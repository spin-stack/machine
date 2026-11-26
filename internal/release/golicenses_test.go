// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// spinMachine is cmd/spin-machine built as the release builds it, once for the package's tests.
var spinMachine string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "release-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spinMachine = filepath.Join(dir, "spin-machine")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", spinMachine, "./cmd/spin-machine")
	cmd.Dir, cmd.Env = filepath.Join("..", ".."), append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building spin-machine: %v: %s\n", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTheGoLicensesAreTheToolchainsAndEachModules(t *testing.T) {
	root := filepath.Join("..", "..")
	text, err := GoLicenses(context.Background(), root, spinMachine)
	if err != nil {
		t.Fatal(err)
	}
	goroot, _ := goOutput(context.Background(), root, "env", "GOROOT")
	for _, want := range []string{
		"The licences of the Go code linked into bin/spin-machine",
		"\n==> Go ", ", its standard library\n\n" + string(mustRead(t, filepath.Join(strings.TrimSpace(goroot), "LICENSE"))),
		"\n==> golang.org/x/sys v", "Redistribution and use in source and binary forms",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the licences do not say %q:\n%s", want, text)
		}
	}
}

// fakeGoEnv puts a go on the PATH that answers go env with goroot and version, go mod download
// with downloaded, and fails when downloaded is empty.
func fakeGoEnv(t *testing.T, goroot, version, downloaded string) {
	t.Helper()
	dir := t.TempDir()
	put(t, filepath.Join(dir, "downloaded"), downloaded, 0o644)
	put(t, filepath.Join(dir, "go"), "#!/bin/sh\n[ \"$1\" = env ] && printf '%s\\n%s\\n' "+goroot+" "+version+" && exit 0\n"+
		"[ -s "+filepath.Join(dir, "downloaded")+" ] && cat "+filepath.Join(dir, "downloaded")+" && exit 0\necho no >&2; exit 1\n", 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGoLicensesRefuseWhatTheyCannotVouchFor(t *testing.T) {
	ctx := context.Background()
	if _, err := GoLicenses(ctx, t.TempDir(), filepath.Join(t.TempDir(), "none")); err == nil || !strings.Contains(err.Error(), "build info") {
		t.Errorf("no binary: %v", err)
	}
	info, _ := goOutput(ctx, filepath.Join("..", ".."), "version", spinMachine)
	built := strings.Fields(info)[1]

	fakeGoEnv(t, t.TempDir(), "go0.1", "")
	if _, err := GoLicenses(ctx, t.TempDir(), spinMachine); err == nil || !strings.Contains(err.Error(), "built by "+built+", and the go here is go0.1") {
		t.Errorf("another toolchain: %v", err)
	}
	fakeGoEnv(t, t.TempDir(), built, "")
	if _, err := GoLicenses(ctx, t.TempDir(), spinMachine); err == nil || !strings.Contains(err.Error(), "go mod download: exit status 1: no") {
		t.Errorf("modules that do not download: %v", err)
	}
	fakeGoEnv(t, t.TempDir(), built, "{")
	if _, err := GoLicenses(ctx, t.TempDir(), spinMachine); err == nil || !strings.Contains(err.Error(), "reading go mod download's answer") {
		t.Errorf("an answer that is not JSON: %v", err)
	}
	goroot := t.TempDir()
	put(t, filepath.Join(goroot, "LICENSE"), "Go's", 0o644)
	fakeGoEnv(t, goroot, built, `{"Path": "example.com/m", "Version": "v1.0.0", "Dir": "`+t.TempDir()+`"}`)
	if _, err := GoLicenses(ctx, t.TempDir(), spinMachine); err == nil || !strings.Contains(err.Error(), "the licence of example.com/m v1.0.0") {
		t.Errorf("a module with no LICENSE: %v", err)
	}
	// go is asked in root, whose go.mod picks the toolchain: here, a GOROOT that is root.
	root, module := t.TempDir(), t.TempDir()
	put(t, filepath.Join(root, "LICENSE"), "root's licence\n", 0o644)
	put(t, filepath.Join(module, "LICENSE"), "m's licence\n", 0o644)
	fakeGoEnv(t, "$(pwd)", built, `{"Path": "example.com/m", "Version": "v1.0.0", "Dir": "`+module+`"}`)
	if text, err := GoLicenses(ctx, root, spinMachine); err != nil ||
		!strings.Contains(text, "its standard library\n\nroot's licence\n\n==> example.com/m v1.0.0\n\nm's licence\n") {
		t.Errorf("licences read in root: %v\n%s", err, text)
	}
	t.Setenv("PATH", "")
	if _, err := GoLicenses(ctx, t.TempDir(), spinMachine); err == nil || !strings.Contains(err.Error(), "go env") {
		t.Errorf("no go: %v", err)
	}
}
