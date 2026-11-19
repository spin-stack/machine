// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GitHub is this repository on GitHub, through the gh command: its releases, its workflow runs
// and their artifacts, and lab.yml. What asks GitHub is held by TestTheLabTalksToGitHub, the lane
// that reaches it.
//
// Everything goes through the API. `gh run view --log` reaches a host this network does not
// resolve, and the answer to that is the API, never a client told to trust whatever answers.
type GitHub struct {
	Repo string
}

// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// LatestRelease is the newest published release's tag.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) LatestRelease() (string, error) {
	out, err := g.gh("release", "list", "-R", g.Repo, "--exclude-drafts", "--exclude-pre-releases",
		"--limit", "1", "--json", "tagName", "-q", ".[0].tagName")
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(out))
	if tag == "" {
		return "", fmt.Errorf("%s has no published release", g.Repo)
	}
	return tag, nil
}

// DownloadRelease puts release tag's assets matching pattern in dir.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) DownloadRelease(tag, pattern, dir string) error {
	_, err := g.gh("release", "download", tag, "-R", g.Repo, "-p", pattern, "-D", dir)
	return err
}

// DownloadRun puts the artifacts of run id whose names match pattern in dir.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) DownloadRun(id, pattern, dir string) error {
	_, err := g.gh("run", "download", id, "-R", g.Repo, "-p", pattern, "-D", dir)
	return err
}

// Artifact is the artifact name of run id, unzipped into dir.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) Artifact(id, name, dir string) error {
	out, err := g.gh("api", fmt.Sprintf("repos/%s/actions/runs/%s/artifacts", g.Repo, id),
		"--jq", fmt.Sprintf(`.artifacts[] | select(.name == %q) | .id`, name))
	if err != nil {
		return err
	}
	aid := strings.TrimSpace(string(out))
	if aid == "" {
		return fmt.Errorf("run %s has no artifact %s", id, name)
	}
	z, err := g.gh("api", fmt.Sprintf("repos/%s/actions/artifacts/%s/zip", g.Repo, aid))
	if err != nil {
		return err
	}
	r, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		return fmt.Errorf("reading run %s's %s: %w", id, name, err)
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() || strings.Contains(f.Name, "..") {
			continue
		}
		if err := unzipOne(f, filepath.Join(dir, f.Name)); err != nil {
			return err
		}
	}
	return nil
}

// mutate-lane: TestTheLabTalksToGitHub
func unzipOne(f *zip.File, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(to), err)
	}
	in, err := f.Open()
	if err != nil {
		return fmt.Errorf("opening %s in the artifact: %w", f.Name, err)
	}
	defer func() { _ = in.Close() }()
	b, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("reading %s in the artifact: %w", f.Name, err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", to, err)
	}
	return nil
}

// Run is a workflow run, as much of it as the lab asks about.
type Run struct {
	ID         int64     `json:"databaseId"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	CreatedAt  time.Time `json:"createdAt"`
	URL        string    `json:"url"`
}

// LabRuns are lab.yml's newest runs.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) LabRuns() ([]Run, error) {
	out, err := g.gh("run", "list", "-R", g.Repo, "--workflow", "lab.yml", "--limit", "10",
		"--json", "databaseId,status,conclusion,createdAt,url")
	if err != nil {
		return nil, err
	}
	var runs []Run
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, fmt.Errorf("reading lab.yml's runs: %w", err)
	}
	return runs, nil
}

// Dispatch starts lab.yml on ref with inputs, once no other lab run is queued or running - the
// bench fleet measures one at a time, and two runs side by side measure each other - and returns
// the run it started.
//
// mutate-lane: TestTheLabTalksToGitHub, which holds what it asks GitHub; what it decides from the
// answers is busy's and started's.
func (g *GitHub) Dispatch(ctx context.Context, ref string, inputs map[string]string, log io.Writer) (Run, error) {
	for {
		runs, err := g.LabRuns()
		if err != nil {
			return Run{}, err
		}
		url := busy(runs)
		if url == "" {
			break
		}
		_, _ = fmt.Fprintf(log, "waiting for %s to finish: one lab at a time\n", url)
		if err := sleep(ctx, time.Minute); err != nil {
			return Run{}, err
		}
	}
	before := time.Now().Add(-5 * time.Second)
	if _, err := g.gh(dispatchArgs(g.Repo, ref, inputs)...); err != nil {
		return Run{}, err
	}
	for range 30 {
		if err := sleep(ctx, 5*time.Second); err != nil {
			return Run{}, err
		}
		runs, err := g.LabRuns()
		if err != nil {
			return Run{}, err
		}
		if r, ok := started(runs, before); ok {
			return r, nil
		}
	}
	return Run{}, fmt.Errorf("lab.yml was dispatched on %s and no run of it appeared", ref)
}

// busy is a lab run that is queued or running, by its URL, or "" when there is none.
func busy(runs []Run) string {
	for _, r := range runs {
		if r.Status != "completed" {
			return r.URL
		}
	}
	return ""
}

// started is the run created after before: the one a dispatch made.
func started(runs []Run, before time.Time) (Run, bool) {
	for _, r := range runs {
		if r.CreatedAt.After(before) {
			return r, true
		}
	}
	return Run{}, false
}

// dispatchArgs is gh's command line for lab.yml on ref with inputs, in a stable order.
func dispatchArgs(repo, ref string, inputs map[string]string) []string {
	args := []string{"workflow", "run", "lab.yml", "-R", repo, "--ref", ref}
	for _, k := range slices.Sorted(maps.Keys(inputs)) {
		args = append(args, "-f", k+"="+inputs[k])
	}
	return args
}

// Wait returns run id once it has completed.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) Wait(ctx context.Context, id int64) (Run, error) {
	for {
		out, err := g.gh("run", "view", strconv.FormatInt(id, 10), "-R", g.Repo, "--json", "databaseId,status,conclusion,createdAt,url")
		if err != nil {
			return Run{}, err
		}
		var r Run
		if err := json.Unmarshal(out, &r); err != nil {
			return Run{}, fmt.Errorf("reading run %d: %w", id, err)
		}
		if r.Status == "completed" {
			return r, nil
		}
		if err := sleep(ctx, 30*time.Second); err != nil {
			return Run{}, err
		}
	}
}

// mutate-lane: TestTheLabTalksToGitHub
func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
