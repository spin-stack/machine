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

// Releases are the tags of the newest n published releases, newest first.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) Releases(n int) ([]string, error) {
	out, err := g.gh("release", "list", "-R", g.Repo, "--exclude-drafts", "--exclude-pre-releases",
		"--limit", fmt.Sprint(n), "--json", "tagName", "-q", ".[].tagName")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
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
	ID         int64  `json:"databaseId"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// Dispatch starts lab.yml on ref with inputs and returns the run it started, as GitHub's answer
// to the dispatch names it. Runs go at once, each on its own bench instance, and the fleet queues
// past its cap: a comparison is interleaved inside one run, so another run beside it is another
// host and measures nothing of it.
//
// mutate-lane: TestTheLabTalksToGitHub, which holds what it asks GitHub; the request is
// dispatchArgs' and the answer dispatched's.
func (g *GitHub) Dispatch(ref string, inputs map[string]string) (Run, error) {
	out, err := g.gh(dispatchArgs(g.Repo, ref, inputs)...)
	if err != nil {
		return Run{}, err
	}
	return dispatched(out)
}

// dispatched is the run GitHub's answer to a dispatch names.
func dispatched(answer []byte) (Run, error) {
	var d struct {
		ID  int64  `json:"workflow_run_id"`
		URL string `json:"html_url"`
	}
	if err := json.Unmarshal(answer, &d); err != nil || d.ID == 0 {
		return Run{}, fmt.Errorf("lab.yml was dispatched and GitHub named no run: %s", answer)
	}
	return Run{ID: d.ID, Status: "queued", URL: d.URL}, nil
}

// dispatchArgs is gh's request for lab.yml on ref with inputs, in a stable order, asking GitHub
// for the run it starts.
func dispatchArgs(repo, ref string, inputs map[string]string) []string {
	args := []string{"api", "-X", "POST", "repos/" + repo + "/actions/workflows/lab.yml/dispatches",
		"-f", "ref=" + ref, "-F", "return_run_details=true"}
	for _, k := range slices.Sorted(maps.Keys(inputs)) {
		args = append(args, "-f", "inputs["+k+"]="+inputs[k])
	}
	return args
}

// Wait returns run id once it has completed.
//
// mutate-lane: TestTheLabTalksToGitHub
func (g *GitHub) Wait(ctx context.Context, id int64) (Run, error) {
	for {
		out, err := g.gh("run", "view", strconv.FormatInt(id, 10), "-R", g.Repo, "--json", "databaseId,status,conclusion,url")
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
