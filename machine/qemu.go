// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// QEMU is a machine running under QEMU, reached through one of its monitors. It is one caller's
// at a time: Target keeps what it read without a lock, and nothing orders what two callers would
// do to the machine at once.
type QEMU struct {
	c *qmp.Client
	// record is what Target read, once it has.
	record *Target
	// polling is whether GuestMemory has turned the balloon's reports on. Atomic because spin's
	// runner reads the guest's memory from more than one goroutine.
	polling atomic.Bool
	// then is the Phases of the checkpoint or fast resume whose last phase another call tells:
	// Continue or Hold after a checkpoint, Loaded after a fast resume. data is the fast resume's
	// state's data, for the StateLoaded Loaded tells.
	then func(Phase)
	data int64
}

var _ VM = (*QEMU)(nil)

// Attach takes over a QMP client of a machine started from a Spec's Args, and returns the
// machine. It asks QEMU nothing. The client's connection has to pass descriptors, as qmp.Unix
// does, because disks and states are handed to QEMU as descriptors over it.
//
// It takes no Spec: what it needs - which places for disks the machine has and what each is,
// whether it is held for a boot or for a checkpoint, its memory and vCPU slots - it asks QEMU,
// which cannot disagree with the command line it was started from the way a second copy of the
// Spec could.
func Attach(c *qmp.Client) *QEMU { return &QEMU{c: c} }

// Dial connects to the monitor listening at socket, of a machine started elsewhere or a second
// monitor of one Start started, and returns the machine. ctx bounds the connect and the handshake,
// which waits for QEMU's start when Start bound the socket before QEMU ran. A socket nobody listens
// on is an error at once: Dial waits for no one to make it.
func Dial(ctx context.Context, socket string, o qmp.Observer) (*QEMU, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to the machine at %s: %w", socket, err)
	}
	c, err := qmp.New(ctx, qmp.Unix(conn.(*net.UnixConn)), o)
	if err != nil {
		return nil, fmt.Errorf("talking to the machine at %s: %w", socket, err)
	}
	return Attach(c), nil
}

// Close lets the monitor go. The machine runs on: a monitor's client leaving is nothing to QEMU.
func (m *QEMU) Close() error { return m.c.Close() }

var (
	quit        = qmpdef.Define[qmp.None, qmp.None]("quit")
	queryStatus = qmpdef.Define[qmp.None, runStatus]("query-status")
	// deviceAdd's arguments are a device's driver, its id and its own properties, which QEMU's
	// schema does not list: device_add takes any property the device has. So a map is what they
	// are, and Check holds the command, not the properties, to QEMU.
	deviceAdd         = qmpdef.Define[map[string]any, qmp.None]("device_add")
	deviceDel         = qmpdef.Define[deviceID, qmp.None]("device_del")
	deviceDeleted     = qmpdef.DefineEvent[deviceGone]("DEVICE_DELETED")
	blockdevSetActive = qmpdef.Define[activeArgs, qmp.None]("blockdev-set-active")
	systemPowerdown   = qmpdef.Define[qmp.None, qmp.None]("system_powerdown")
	shutdownEvent     = qmpdef.DefineEvent[End]("SHUTDOWN")
)

// deviceGone is DEVICE_DELETED's data: the id of the device gone, which a device added without one
// has none of.
type deviceGone struct {
	Device string `json:"device"`
}

type runStatus struct {
	Status Status `json:"status"`
}

// Status is QEMU's run state. The ones named here are the ones the machine and its callers tell
// apart. QEMU has others, which are passed on as they are.
type Status string

const (
	// Prelaunch is a machine held for a boot (-S), which Boot starts.
	Prelaunch Status = "prelaunch"
	// Inmigrate is a machine held for a checkpoint (-incoming defer), which Resume loads.
	Inmigrate Status = "inmigrate"
	// Running is a machine whose vCPUs run.
	Running Status = "running"
	// Paused is a machine stopped: by a stop, or resumed from a checkpoint of a stopped one.
	Paused Status = "paused"
	// Postmigrate is a machine a completed checkpoint left stopped, its disks let go until Hold
	// or Continue takes them back.
	Postmigrate Status = "postmigrate"
)

func init() { qmpdef.Enum(Prelaunch, Inmigrate, Running, Paused, Postmigrate) }

type deviceID struct {
	ID string `json:"id"`
}

type activeArgs struct {
	Active bool `json:"active"`
}

// End is how QEMU said the machine ended: the data of the last SHUTDOWN it sent.
type End struct {
	// Guest is whether the guest asked for the end, and not the host.
	Guest bool `json:"guest"`
	// Reason is why, as QEMU names it.
	Reason ShutdownCause `json:"reason"`
}

// ShutdownCause is SHUTDOWN's reason. The ones named here are the ones spin's runner explains an
// exit by. QEMU has others, which are passed on as they are.
type ShutdownCause string

const (
	// GuestShutdown is a guest that powered itself off.
	GuestShutdown ShutdownCause = "guest-shutdown"
	// GuestReset is a guest that rebooted, which -no-reboot (Spec.Args) makes an end. A panic
	// is one too: panic=1 reboots the guest a second after it.
	GuestReset ShutdownCause = "guest-reset"
	// GuestPanic is a panic a panic device reported. This machine has none, and says GuestReset
	// for a panic.
	GuestPanic ShutdownCause = "guest-panic"
	// HostSignal is a QEMU ended by a signal.
	HostSignal ShutdownCause = "host-signal"
)

func init() { qmpdef.Enum(GuestShutdown, GuestReset, GuestPanic, HostSignal) }

// Quit ends QEMU. Its reply may never arrive, because QEMU can close the monitor before sending
// it, so a connection that ends instead is the quit having worked.
func (m *QEMU) Quit(ctx context.Context) error {
	if _, err := qmp.Do(ctx, m.c, quit, qmp.None{}); err != nil && !errors.Is(err, qmp.ErrClosed) {
		return fmt.Errorf("quitting: %w", err)
	}
	return nil
}

// Status is the machine's run state.
func (m *QEMU) Status(ctx context.Context) (Status, error) {
	st, err := qmp.Do(ctx, m.c, queryStatus, qmp.None{})
	if err != nil {
		return "", fmt.Errorf("asking the machine's run state: %w", err)
	}
	return st.Status, nil
}

// Hold takes back the disks of a machine a checkpoint left stopped (Postmigrate), and leaves it
// stopped. A completed migration lets the source's disks go, for the machine that loads it, and
// cont takes them back only by running the guest. Continue runs it after.
func (m *QEMU) Hold(ctx context.Context) error {
	if _, err := qmp.Do(ctx, m.c, blockdevSetActive, activeArgs{Active: true}); err != nil {
		return fmt.Errorf("taking the machine's disks back: %w", err)
	}
	tell(m.then, GuestHeld{At: time.Now()})
	m.then = nil
	return nil
}

// Done is closed once the monitor's connection has ended: when QEMU has exited, or on Close.
func (m *QEMU) Done() <-chan struct{} { return m.c.Done() }

// End is the last SHUTDOWN QEMU sent, and whether it sent one. QEMU sends it as the machine ends,
// before it closes the monitor, so once Done is closed End has it if QEMU said it.
//
// Under -no-reboot a guest's reboot and its panic both end as GuestReset, and only the console
// tells them apart. A machine that ended without a SHUTDOWN, as one killed by SIGKILL does, has
// none.
func (m *QEMU) End() (End, bool) { return qmp.Last(m.c, shutdownEvent) }

// Shutdown presses the machine's ACPI power button, which systemd answers by powering the guest
// off, and returns once QEMU has pressed it, not once the guest is down: Done and End say that,
// End as GuestShutdown. A guest whose init does not answer the button runs on.
//
// Not ctrl-alt-del, which spin's runner sent: QEMU takes the keys and the guest never sees them,
// because its kernel has no i8042 or AT keyboard driver (kernel/config-7.3-x86_64). Booted to
// systemd, a machine sent the keys ran on until it was killed, and one whose button was pressed
// powered off 0.64 s later (laptop, QEMU 11.2.0-rc0, v20261120.04's image, 2026-11-21).
func (m *QEMU) Shutdown(ctx context.Context) error {
	if _, err := qmp.Do(ctx, m.c, systemPowerdown, qmp.None{}); err != nil {
		return fmt.Errorf("pressing the power button: %w", err)
	}
	return nil
}

// unplug removes the device id and returns once QEMU has reported it gone. The wait is armed
// before device_del, whose answer the event may come with, and sees no DEVICE_DELETED of an
// earlier device with the same id.
func (m *QEMU) unplug(ctx context.Context, id string) error {
	gone := qmp.Expect(m.c, deviceDeleted, func(d deviceGone) bool { return d.Device == id })
	defer gone.Stop()
	if _, err := qmp.Do(ctx, m.c, deviceDel, deviceID{id}); err != nil {
		return err
	}
	if _, err := gone.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for %s to go: %w", id, err)
	}
	return nil
}

// Boot binds the disks to their places and lets the vCPUs go.
func (m *QEMU) Boot(ctx context.Context, disks ...Disk) error {
	st, err := m.Status(ctx)
	if err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if st != Prelaunch {
		// inmigrate takes a cont and stays inmigrate: QEMU only notes that the machine is to
		// run once a migration has arrived. A machine for a boot is started without
		// Spec.Restore.
		return fmt.Errorf("booting a machine that is %s: only one started held for a boot boots", st)
	}
	if err := m.bindAll(ctx, disks, nil); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if _, err := qmp.Do(ctx, m.c, qmp.Cont, qmp.None{}); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	return nil
}

// Continue runs a machine a checkpoint left stopped.
func (m *QEMU) Continue(ctx context.Context) error {
	if _, err := qmp.Do(ctx, m.c, qmp.Cont, qmp.None{}); err != nil {
		return fmt.Errorf("continuing the machine: %w", err)
	}
	tell(m.then, GuestRunning{At: time.Now()})
	m.then = nil
	return nil
}

const peripheral = "/machine/peripheral/"
