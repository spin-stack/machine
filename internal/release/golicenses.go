// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GoLicenses is the licence text of the code the Go binary at path links besides this repository's:
// Go's standard library and each module its build info lists, read from the toolchain and the
// module cache root builds with. Their BSD-style licences ask that the text go with a binary, and
// an SBOM names a licence without carrying it. A module with no LICENSE fails it, and so does a
// toolchain that is not the one that built the binary, whose licence would be another's.
func GoLicenses(ctx context.Context, root, path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s's build info: %w", path, err)
	}
	env, err := goOutput(ctx, root, "env", "GOROOT", "GOVERSION")
	if err != nil {
		return "", err
	}
	goroot, goversion, _ := strings.Cut(strings.TrimSpace(env), "\n")
	if goversion != info.GoVersion {
		return "", fmt.Errorf("%s was built by %s, and the go here is %s", filepath.Base(path), info.GoVersion, goversion)
	}
	type section struct{ name, dir string }
	sections := []section{{"Go " + strings.TrimPrefix(goversion, "go") + ", its standard library", goroot}}
	if len(info.Deps) > 0 {
		args := []string{"mod", "download", "-json"}
		for _, d := range info.Deps {
			if d.Replace != nil {
				d = d.Replace
			}
			args = append(args, d.Path+"@"+d.Version)
		}
		out, err := goOutput(ctx, root, args...)
		if err != nil {
			return "", err
		}
		dec := json.NewDecoder(strings.NewReader(out))
		for {
			var m struct{ Path, Version, Dir string }
			if err := dec.Decode(&m); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return "", fmt.Errorf("reading go mod download's answer: %w", err)
			}
			sections = append(sections, section{m.Path + " " + m.Version, m.Dir})
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The licences of the Go code linked into bin/%s, besides this repository's own (LICENSE).\n", filepath.Base(path))
	for _, s := range sections {
		text, err := os.ReadFile(filepath.Join(s.dir, "LICENSE"))
		if err != nil {
			return "", fmt.Errorf("the licence of %s: %w", s.name, err)
		}
		fmt.Fprintf(&b, "\n==> %s\n\n%s", s.name, text)
	}
	return b.String(), nil
}

// goOutput is what go prints, run in root so that its go.mod picks the toolchain.
func goOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %w: %s", strings.Join(args[:min(len(args), 2)], " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return string(out), nil
}
