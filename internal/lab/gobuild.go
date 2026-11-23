// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	xssh "golang.org/x/crypto/ssh"
)

// goBuild is what nesting costs a build a workspace runs (#563): the Go toolchain building cmd/go
// from its own source with an empty build cache, on a workspace disk attached after the boot, as
// spin hands a workspace its filesystem. It is compile-bound with the reads and writes a build
// makes, and needs nothing the image lacks but the toolchain, which the disk carries.
//
// The toolchain is the host's own (`go env GOROOT`), which go.mod's toolchain line pins and the
// checksum database verified when it was downloaded, so the nested run and the metal run of one
// commit build with the same compiler. The disk is a raw ext4 image on the host's tmpfs, for the
// reason the io probe's is.
type goBuild struct{}

func (goBuild) Name() string { return "build" }
func (goBuild) About() string {
	return "`go build cmd/go` inside the guest with an empty cache, on an ext4 disk on the host's tmpfs attached after the boot, `sync` included"
}

func (goBuild) Options() []Option { return shape }

func (goBuild) Rows(Options) []string { return []string{"cmd/go"} }
func (goBuild) Phases() int           { return 1 }

func (goBuild) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "console"}, Need{"boot", "append"},
		Need{"boot", "qmp"}, Need{"attach", "disk-serial"}), plugNeeds(o)...)
}

func (goBuild) Metrics() []Metric {
	return []Metric{
		{"wall", "ms", "the build and its sync, on the guest's clock"},
		{"host_cpu", "ms", "QEMU's CPU time over the build, every thread"},
		{"guest_cpu", "ms", "the guest's busy time over the build, all vCPUs"},
	}
}

// goDisk makes the workspace disk a build starts from at path: a raw ext4 image holding the
// host's Go toolchain at /go. It returns the toolchain's version.
func goDisk(path string) (string, error) {
	out, err := exec.Command("go", "env", "GOROOT", "GOVERSION").Output()
	f := strings.Fields(string(out))
	if err != nil || len(f) != 2 {
		return "", fmt.Errorf("asking go for its GOROOT and version: %v %q", err, out)
	}
	tree := path + ".tree"
	if err := os.Mkdir(tree, 0o755); err != nil {
		return "", fmt.Errorf("making %s: %w", tree, err)
	}
	if err := run("cp", "-r", f[0], filepath.Join(tree, "go")); err != nil {
		return "", err
	}
	if err := run("mkfs.ext4", "-q", "-F", "-E", "root_owner=0:0", "-d", tree, path, "4G"); err != nil {
		return "", err
	}
	// The module cache's toolchain is read-only, and so is its copy.
	return f[1], run("sh", "-c", `chmod -R u+w "$1" && rm -rf "$1"`, "-", tree)
}

// mutate-lane: TestEveryProbeMeasuresATree
func (goBuild) Measure(_ context.Context, m *Measurement) error {
	dir, remove, err := tmpfsDir()
	if err != nil {
		return err
	}
	defer remove()
	disk := filepath.Join(dir, "workspace.raw")
	version, err := goDisk(disk)
	if err != nil {
		return err
	}
	mc, cid, _, err := m.bootLoggedIn("build.qcow2")
	if mc != nil {
		defer mc.Stop()
	}
	if err != nil {
		return err
	}
	dev, err := m.attachRaw(cid, "build.qcow2", disk, "ws")
	if err != nil {
		return err
	}
	env := "export GOROOT=/mnt/ws/go PATH=/mnt/ws/go/bin:$PATH GOCACHE=/mnt/ws/cache GOTMPDIR=/mnt/ws/tmp GOPATH=/mnt/ws/gopath GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS="
	if _, _, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, "mkdir -p /mnt/ws && mount "+dev+" /mnt/ws && mkdir -p /mnt/ws/tmp && "+env+" && test \"$(go env GOVERSION)\" = "+version); err != nil {
		return Miss("mounting the workspace disk and finding %s on it: %v", version, err)
	}
	script := env + "; head -1 /proc/stat; s=$(date +%s%N); go build -o /mnt/ws/out cmd/go >&2 && sync && e=$(date +%s%N) && echo $(( (e - s) / 1000 )); head -1 /proc/stat"
	host0, err := qemuCPU(mc.PID())
	if err != nil {
		return err
	}
	_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, script)
	if err != nil {
		return Miss("go build cmd/go: %v", err)
	}
	host1, err := qemuCPU(mc.PID())
	if err != nil {
		return err
	}
	guest, between, err := guestBusyAround(out)
	if err != nil {
		return Miss("go build cmd/go: %v", err)
	}
	us, err := strconv.ParseInt(strings.TrimSpace(between), 10, 64)
	if err != nil {
		return Miss("go build cmd/go said no time: %q", between)
	}
	m.Record("wall", float64(us)/1000)
	m.Record("host_cpu", ms(host1-host0))
	m.Record("guest_cpu", ms(guest))
	m.Logf("go build cmd/go (%s): %d ms, host CPU %.0f ms, guest CPU %.0f ms", version, us/1000, ms(host1-host0), ms(guest))
	return nil
}
