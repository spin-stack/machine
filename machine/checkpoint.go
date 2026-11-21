// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// LoadOptions is how a checkpoint is read.
type LoadOptions struct {
	// Fast runs the guest as soon as its devices are loaded and reads its memory as it touches
	// it, the rest behind it. Without Fast the guest runs once all of it has been read.
	//
	// Slower to the first login on the bench fleet (nested, QEMU 11.2.0-rc0, a 140 MiB state read
	// from disk, 2026-11-19, lab 44585840498): running at 38 ms instead of 102, logged in at 293
	// instead of 152. Every page the guest touches on the way to a login is a fault served by one
	// read from the file, and a nested fault is expensive; the full load reads the file in one
	// pass. It is kept for a caller whose state is large and whose guest needs little of it soon.
	Fast bool
	// Channels is how many threads read the guest's memory, each its own part of the file: QEMU's
	// multifd over a mapped-ram state. One is the migration's own thread; none given is
	// DefaultChannels for a full load of a mapped-ram state, and one for a fast load or a stream
	// state, which one thread reads.
	Channels int
}

// SaveOptions is how a checkpoint is written.
type SaveOptions struct {
	// Channels is how many threads write the guest's memory, as LoadOptions.Channels reads it;
	// none given is DefaultChannels. The state is the same file whatever wrote it, and loads with
	// any number.
	Channels int
}

// DefaultChannels is how many threads move a checkpoint's memory when SaveOptions or LoadOptions
// names none. Eight, not sixteen, because restores share a host: one restore into a QEMU started
// ahead reached the first login at 152 ms with 1 channel, 100 with 8 and 90 with 16, but four at
// once read 184, 140 and 143, 16 losing to 8 by 3.2 ms [1.1, 5.8], and eight at once 400, 329 and
// 337 (bench, spin's shape and login, 30 bursts, lab 44792429636, 2026-11-20). Each channel is a
// thread of QEMU's.
const DefaultChannels = 8

// channels is how many threads write s's state.
func (s SaveOptions) channels() int {
	if s.Channels == 0 {
		return DefaultChannels
	}
	return s.Channels
}

// channels is how many threads read a state in format with l.
func (l LoadOptions) channels(format string) int {
	switch {
	case l.Channels != 0:
		return l.Channels
	case l.Fast || format == FormatStream:
		return 1
	}
	return DefaultChannels
}

// Saved is what a checkpoint is besides its state file: what the caller keeps beside the state
// and hands back to Resume.
type Saved struct {
	// Format names how the state file is written. A VM resumes only formats it reads, and
	// says so before reading the file.
	Format string `json:"format"`
	// From is the machine the checkpoint was taken on, as it said at the checkpoint (VM.Target):
	// what CanResume holds the machine it resumes into to.
	From Target `json:"from"`
	// Added is what the VMM adds again before the state is loaded - the vCPUs and the disks added
	// while the guest ran - in a form only the VMM reads. It is not the machine: From is what the
	// machine was started with.
	Added json.RawMessage `json:"added,omitempty"`
}

// Checkpoint formats. A QEMU without mapped-ram, or told to load without it, cannot read a
// FormatMappedRAM state whatever the machine is: so the format is recorded with the state (Saved)
// and checked before a byte is read.
const (
	// FormatMappedRAM is QEMU's migration stream with mapped-ram: each RAM page at a fixed
	// offset of the file, which is what lets a load read a page when the guest touches it
	// (LoadOptions.Fast). The file is sparse, as large as the machine's RAM blocks at their
	// ceilings; what is not written is holes.
	FormatMappedRAM = "qemu-mapped-ram"
	// FormatStream is the stream before it, every page in order: what a checkpoint saved by
	// a spin-machine before mapped-ram is, which still loads, but only in full.
	FormatStream = "qemu-stream"
)

// added is what a QEMU checkpoint adds again before its state is loaded (Saved.Added).
type added struct {
	CPUs  []addedCPU `json:"cpus,omitempty"`
	Disks []hotDisk  `json:"disks,omitempty"`
}

// checkpointBandwidth lifts QEMU's default migration cap, 128 MiB/s, which is meant for a
// network: a checkpoint is a local file.
const checkpointBandwidth = int64(64) << 30

var (
	migrateOut             = qmpdef.Define[migrateArgs, qmp.None]("migrate")
	migrateIn              = qmpdef.Define[migrateArgs, qmp.None]("migrate-incoming")
	migrateSetCapabilities = qmpdef.Define[capabilities, qmp.None]("migrate-set-capabilities")
	migrateSetParameters   = qmpdef.Define[migrationParameters, qmp.None]("migrate-set-parameters")
	migrateContinue        = qmpdef.Define[migrationState, qmp.None]("migrate-continue")
	migrateCancel          = qmpdef.Define[qmp.None, qmp.None]("migrate_cancel")
	queryMigrate           = qmpdef.Define[qmp.None, migrationInfo]("query-migrate")
)

type migrateArgs struct {
	URI string `json:"uri"`
}

// migrationCapability is a capability of QEMU's migration the machine sets.
type migrationCapability string

const (
	mappedRAM             migrationCapability = "mapped-ram"
	pauseBeforeSwitchover migrationCapability = "pause-before-switchover"
	postcopyRAM           migrationCapability = "postcopy-ram"
	multifd               migrationCapability = "multifd"
)

type capabilities struct {
	Capabilities []capabilityState `json:"capabilities"`
}

type capabilityState struct {
	Capability migrationCapability `json:"capability"`
	State      bool                `json:"state"`
}

// migrationParameters are the parameters the machine sets: each left as it was when not given.
type migrationParameters struct {
	MaxBandwidth    int64 `json:"max-bandwidth,omitempty"`
	MultifdChannels int   `json:"multifd-channels,omitempty"`
}

// migrationStatus is a state of a migration, of the ones the machine waits on or refuses.
type migrationStatus string

const (
	preSwitchover  migrationStatus = "pre-switchover"
	postcopyActive migrationStatus = "postcopy-active"
	completed      migrationStatus = "completed"
	failed         migrationStatus = "failed"
	cancelled      migrationStatus = "cancelled"
)

func init() {
	qmpdef.Enum(mappedRAM, pauseBeforeSwitchover, postcopyRAM, multifd)
	qmpdef.Enum(preSwitchover, postcopyActive, completed, failed, cancelled)
}

type migrationState struct {
	State migrationStatus `json:"state"`
}

type migrationInfo struct {
	Status    migrationStatus `json:"status"`
	ErrorDesc string          `json:"error-desc"`
}

// Checkpoint saves the machine to a file in FormatMappedRAM, its memory written by save.Channels
// threads, DefaultChannels when it names none.
//
// pause-before-switchover stops the migration with the guest paused and the disks quiet, after
// the last RAM is written and before the device state is: seal runs there. The vCPUs and disks
// added while the machine ran are read before the migration starts, because QEMU refuses a query
// of some of them once one is under way. The machine's record is read while it runs: QEMU answers
// its monitors while it writes the guest's RAM.
func (m *QEMU) Checkpoint(ctx context.Context, to *os.File, save SaveOptions, seal func(context.Context) error) (Saved, error) {
	var a added
	var err error
	if a.CPUs, err = m.addedCPUs(ctx); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	if a.Disks, err = m.hotDisks(ctx); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	again, err := json.Marshal(a)
	if err != nil {
		return Saved{}, fmt.Errorf("checkpointing: recording what was added: %w", err)
	}
	channels := save.channels()
	caps := map[migrationCapability]bool{mappedRAM: true, pauseBeforeSwitchover: true, postcopyRAM: false, multifd: channels > 1}
	if err := m.setCapabilities(ctx, caps); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	params := migrationParameters{MaxBandwidth: checkpointBandwidth}
	if channels > 1 {
		params.MultifdChannels = channels
	}
	if _, err := do(ctx, m.q, migrateSetParameters, params); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	writing := func() error {
		// Read before the migration, the record's round trips made `spin-machine save` 7.0 and
		// 10.6 ms slower (bench, lab 44866054104); read here they overlap the RAM being written,
		// and the record is the same (laptop, 2026-11-21). Target keeps what it read, and is
		// asked again below, where an error counts.
		_, _ = m.Target(ctx)
		return m.switchover(ctx, seal)
	}
	if err := m.migrate(ctx, migrateOut, to, os.O_WRONLY, writing); err != nil {
		// A migration left at pre-switchover holds the guest paused with nobody to continue
		// it; a cancel with none running succeeds.
		_, cancelErr := do(context.WithoutCancel(ctx), m.q, migrateCancel, qmp.None{})
		return Saved{}, errors.Join(fmt.Errorf("checkpointing: %w", err), cancelErr)
	}
	from, err := m.Target(ctx)
	if err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	return Saved{Format: FormatMappedRAM, From: from, Added: again}, nil
}

// switchover runs seal at pre-switchover and waits for the migration to complete.
func (m *QEMU) switchover(ctx context.Context, seal func(context.Context) error) error {
	for {
		st, err := m.migration(ctx)
		if err != nil {
			return err
		}
		switch st {
		case completed:
			return nil
		case preSwitchover:
			if err := seal(ctx); err != nil {
				return fmt.Errorf("sealing: %w", err)
			}
			if _, err := do(ctx, m.q, migrateContinue, migrationState{State: preSwitchover}); err != nil {
				return err
			}
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// migration is the state of the migration in flight, either direction, or an error for one that
// failed.
func (m *QEMU) migration(ctx context.Context) (migrationStatus, error) {
	info, err := do(ctx, m.q, queryMigrate, qmp.None{})
	if err != nil {
		return "", err
	}
	if info.Status == failed || info.Status == cancelled {
		return "", fmt.Errorf("the migration %s: %s", info.Status, cmp.Or(info.ErrorDesc, "QEMU said nothing more"))
	}
	return info.Status, nil
}

func (m *QEMU) setCapabilities(ctx context.Context, caps map[migrationCapability]bool) error {
	var list []capabilityState
	for _, c := range slices.Sorted(maps.Keys(caps)) {
		list = append(list, capabilityState{Capability: c, State: caps[c]})
	}
	if _, err := do(ctx, m.q, migrateSetCapabilities, capabilities{Capabilities: list}); err != nil {
		return fmt.Errorf("setting the migration's capabilities: %w", err)
	}
	return nil
}

// migrate runs migrate or migrate-incoming over f, handed to QEMU as a descriptor set, and then
// done, which waits for the migration. The set is removed once done returns, not once the command
// is in: a multifd channel opens the file again from the set, and a save's channels open theirs on
// QEMU's migration thread, after the command has returned. QEMU frees the set when it closes the
// last descriptor it took from it.
//
// f is reopened for access, because QEMU takes from a set only a descriptor whose access mode
// is the one it opens the file with - write-only to save, read-only to load ("Failed to find file
// descriptor with matching flags") - and the caller's file need not be either.
func (m *QEMU) migrate(ctx context.Context, command qmp.Command[migrateArgs, qmp.None], f *os.File, access int, done func() error) error {
	if f == nil {
		return errors.New("no file for the state")
	}
	g, err := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", f.Fd()), access, 0) // mutate-exempt: the mode is read only with O_CREATE, which access never has
	if err != nil {
		return fmt.Errorf("reopening the state's file for QEMU: %w", err)
	}
	defer func() { _ = g.Close() }() // QEMU holds its own copy
	set, err := do(ctx, m.q, qmp.AddFD, qmp.AddFDArgs{File: g, Opaque: "state"})
	if err != nil {
		return fmt.Errorf("handing state to QEMU: %w", err)
	}
	_, err = do(ctx, m.q, command, migrateArgs{URI: "file:" + set.Path()})
	if err == nil {
		err = done()
	}
	if _, rmErr := do(ctx, m.q, qmp.RemoveFD, qmp.RemoveFDArgs{Set: set.Set}); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Resume loads a checkpoint into a machine started with Spec.Restore, once CanResume(saved, here)
// allows it. That comes first, so a checkpoint of another machine is refused before QEMU is asked
// anything, and before a byte of its state is read.
//
// here is this machine's Target, and Resume trusts it: it does not ask QEMU. The caller is
// responsible for here describing this machine. spin reads it once, when its runner starts, from
// a machine started held in the shape it resumes workspaces into, and holds each machine of its
// pool to it once, when it starts it (spin-stack/spin#1416). A caller with no such constant asks
// Target first. Read here, the record cost restore-first's first login +4.3 ms [+0.2, +7.5] with
// the state on the disk and +8.2 [+6.2, +10.8] from the page cache (bench, lab 44868324811,
// 2026-11-21), and no part of it could leave the clock: QEMU's main loop is busy from its exec to
// the load's end, and QEMU answers no monitor while it loads a state.
//
// Fast is QEMU 11.2's fast snapshot load (docs/devel/migration/fast-snapshot-load.rst): mapped-ram
// and postcopy-ram on a file: URI, the guest running once its device state is in while
// userfaultfd serves its faults from the file and an eager thread reads the rest. It returns at
// postcopy-active. QEMU needs userfaultfd for it: /dev/userfaultfd readable and writable by its
// user, or vm.unprivileged_userfaultfd=1, or CAP_SYS_PTRACE - the guest's faults are taken in
// the kernel, which an unprivileged UFFD_USER_MODE_ONLY descriptor does not see.
func (m *QEMU) Resume(ctx context.Context, state *os.File, saved Saved, here Target, load LoadOptions, disks ...Disk) error {
	if err := CanResume(saved, here); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	channels := load.channels(saved.Format)
	caps := map[migrationCapability]bool{pauseBeforeSwitchover: false}
	switch {
	case saved.Format == FormatMappedRAM:
		caps[mappedRAM] = true
		caps[postcopyRAM] = load.Fast
		caps[multifd] = channels > 1
	case saved.Format == FormatStream && load.Fast:
		return errors.New("resuming: a stream checkpoint has no page at a fixed offset to fetch when the guest touches it, and loads only in full")
	case saved.Format == FormatStream && channels > 1:
		return errors.New("resuming: a stream checkpoint is one sequence of pages, which one thread reads")
	case saved.Format == FormatStream:
		caps[mappedRAM] = false
	default:
		return fmt.Errorf("resuming: a checkpoint in format %q, and this machine reads %s and %s", saved.Format, FormatMappedRAM, FormatStream)
	}
	if prefetches(saved, load) {
		prefetch(state)
	}
	var again added
	if len(saved.Added) > 0 {
		if err := json.Unmarshal(saved.Added, &again); err != nil {
			return fmt.Errorf("resuming: reading what the checkpoint says was added: %w", err)
		}
	}
	st, err := m.status(ctx)
	if err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if st != inmigrate {
		return fmt.Errorf("resuming into a machine that is %s: only one started with Spec.Restore takes a checkpoint", st)
	}
	if err := m.bindAll(ctx, disks, again.Disks); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	for _, c := range again.CPUs {
		args, err := c.deviceArgs()
		if err != nil {
			return fmt.Errorf("resuming: %w", err)
		}
		if _, err := do(ctx, m.q, deviceAdd, args); err != nil {
			return fmt.Errorf("resuming: adding vCPU %s: %w", c.ID, err)
		}
	}
	if err := m.setCapabilities(ctx, caps); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if channels > 1 {
		if _, err := do(ctx, m.q, migrateSetParameters, migrationParameters{MultifdChannels: channels}); err != nil {
			return fmt.Errorf("resuming: %w", err)
		}
	}
	running := func() error {
		for {
			st, err := m.migration(ctx)
			if err != nil {
				return err
			}
			if st == completed || (load.Fast && st == postcopyActive) {
				return nil
			}
			if err := sleep(ctx, pollInterval); err != nil {
				return err
			}
		}
	}
	if err := m.migrate(ctx, migrateIn, state, os.O_RDONLY, running); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	// A machine saved stopped comes back stopped: QEMU carries the source's run state across.
	if st, err = m.status(ctx); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if st == paused {
		return m.Continue(ctx)
	}
	return nil
}

// prefetches is whether a resume reads the state ahead: only a full load of a mapped-ram state.
// A fast load reads a page when the guest touches it, which reading every page ahead would undo,
// and a stream state is read once, in order, by the load itself.
func prefetches(saved Saved, load LoadOptions) bool {
	return saved.Format == FormatMappedRAM && !load.Fast
}

// prefetch asks the kernel to start reading the state's data into the page cache, extent by
// extent, while the machine is still being told what to load: the load reads every page of data
// anyway, and from a state out of the page cache it waits on the disk. QEMU's channels share one
// open file and its readahead, and each reads the pages it is handed in turn; asked for here, the
// data is read in the file's order, in large requests, from the moment Resume starts.
//
// It is best effort and returns at once: data the kernel does not read ahead is read by the load
// as before.
func prefetch(state *os.File) {
	const prefetchChunk = 128 << 10 // mutate-exempt: the size of each request is speed, not what is read; the loop asks for every byte of each extent whatever it is
	f, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", state.Fd()))
	if err != nil {
		return
	}
	go func() {
		defer func() { _ = f.Close() }()
		fd := int(f.Fd())
		for off := int64(0); ; { // mutate-exempt: SEEK_DATA from 1 finds the data the page at 0 starts, so a start of 1 reads the same pages
			start, err := unix.Seek(fd, off, unix.SEEK_DATA)
			if err != nil {
				return
			}
			end, err := unix.Seek(fd, start, unix.SEEK_HOLE)
			if err != nil {
				return
			}
			// The kernel reads at most the device's readahead window of one request (128 KiB
			// unless the disk says more) and drops the rest, so the extent is asked for in pieces.
			for off = start; off < end; off += prefetchChunk {
				_ = unix.Fadvise(fd, off, min(prefetchChunk, end-off), unix.FADV_WILLNEED) // mutate-exempt: past the extent's end a request reads the hole after it, which holds nothing; what is read of the data is the same
			}
		}
	}()
}

// Loaded returns once the load is complete.
func (m *QEMU) Loaded(ctx context.Context) error {
	for {
		st, err := m.migration(ctx)
		if err != nil {
			return fmt.Errorf("finishing the load: %w", err)
		}
		if st == completed {
			return nil
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return fmt.Errorf("finishing the load: %w", err)
		}
	}
}
