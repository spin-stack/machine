// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spin-stack/machine/machine"
)

// seccompUnits are the units whose sandbox the first login waits for (#574, #637).
var seccompUnits = []string{"systemd-journald", "systemd-logind", "user@0"}

// A correctness test: each of seccompUnits denies the system calls the release's denies, and
// with the same answer. boot/seccompprobe runs as the unit's ExecStartPost=, which the executor
// starts with the unit's sandbox, and makes every call; its table must be
// testdata/seccomp-table.txt, the release's. A change to systemd's executor or to a unit's
// sandbox that changes what a unit may call fails here. Without the file it fails with the table
// this tree gives, which is how a meant change records its new reference. The filter counts are
// logged, not compared: how many filters carry the policy is what #637 changes.
func TestSeccompDeniesWhatTheReleaseDenies(t *testing.T) {
	if os.Getenv("SPIN_SECCOMP_TEST") != "1" {
		t.Skip("set SPIN_SECCOMP_TEST=1 to compare the units' seccomp tables in a built image")
	}
	out := releaseTree(t)
	rel, err := machine.OpenRelease(out)
	if err != nil {
		t.Fatal(err)
	}
	base, err := rel.Rootfs()
	if err != nil {
		t.Fatal(err)
	}
	root := newRawRoot(t, out, base)
	probe := filepath.Join(t.TempDir(), "seccompprobe")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", probe, "./seccompprobe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building seccompprobe: %v\n%s", err, b)
	}
	body, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	root.write("/usr/local/libexec/spin-seccomp-probe", string(body))
	root.edit("sif /usr/local/libexec/spin-seccomp-probe mode 0100755")
	for _, u := range seccompUnits {
		unit := u + ".service"
		if u == "user@0" {
			unit = "user@.service"
		}
		root.write("/etc/systemd/system/"+unit+".d/zz-seccomp-probe.conf",
			"[Service]\nExecStartPost=/usr/local/libexec/spin-seccomp-probe /run/spin-seccomp-"+u+".txt\n")
	}
	oneshotAtBoot(t, root, "seccomp-check", "seccomp-check.sh", "")
	console := checkOnConsole(t, rel, root, "SECCOMP_CHECK_OK", "SECCOMP_CHECK_FAILED")
	for _, m := range regexp.MustCompile(`SECCOMP-FILTERS \S+ main=\d+ probe=\d+`).FindAllString(console, -1) {
		t.Log(m)
	}
	got, err := seccompTables(console)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/seccomp-table.txt")
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no testdata/seccomp-table.txt to compare with; this tree's table:\n%s", got)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the units' seccomp tables are not the release's:\n%s", lineDiff(string(want), got))
	}
}

// seccompTables is each unit's table from the console, a line each call, prefixed by the unit.
func seccompTables(console string) (string, error) {
	var b strings.Builder
	for _, u := range seccompUnits {
		m := regexp.MustCompile(`SECCOMP-TABLE ` + regexp.QuoteMeta(u) + ` ([A-Za-z0-9+/=]+)`).FindStringSubmatch(console)
		if m == nil {
			return "", fmt.Errorf("no table for %s on the console", u)
		}
		gz, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			return "", fmt.Errorf("decoding %s's table: %w", u, err)
		}
		r, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return "", fmt.Errorf("decompressing %s's table: %w", u, err)
		}
		text, err := io.ReadAll(r)
		if err != nil {
			return "", fmt.Errorf("decompressing %s's table: %w", u, err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(text)), "\n") {
			fmt.Fprintf(&b, "%s %s\n", u, l)
		}
	}
	return b.String(), nil
}

// lineDiff is the lines of want missing from got and of got missing from want.
func lineDiff(want, got string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var b strings.Builder
	for _, l := range strings.Split(want, "\n") {
		if !g[l] {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if !w[l] {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	return b.String()
}
