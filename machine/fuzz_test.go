// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
)

// What a checkpoint's caller hands back to Resume is bytes it kept beside a state file, stored
// and copied by spin and read here (#653). These fuzz targets hold what Saved, its record and
// CanResume promise of any bytes that read as one. `task fuzz` runs each for a bounded time, and
// a failing input it finds lands in testdata/fuzz, where go test runs it from then on.

// savedSeeds are records of the shapes CanResume's tests use: spin's machine, one from before
// the vCPU's properties were held, one with vCPUs and disks added, and an empty one.
func savedSeeds(f *testing.F) [][]byte {
	f.Helper()
	spin := target(f, nil)
	old := target(f, func(t *Target, _ facts) { t.CPU.Properties = nil })
	added := json.RawMessage(`{"cpus":[{"id":"cpu-1","driver":"host-x86_64-cpu","props":{"socket-id":1,"core-id":0,"thread-id":0}}],"disks":[{"serial":"ext","target":3}]}`)
	var seeds [][]byte
	for _, s := range []Saved{
		{Format: FormatMappedRAM, From: spin, Length: 1 << 30},
		{Format: FormatStream, From: old},
		{Format: FormatMappedRAM, From: spin, Added: added},
		{},
	} {
		b, err := json.Marshal(s)
		if err != nil {
			f.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	return seeds
}

// Any bytes that read as a Saved write one form, read back to the same record, and resume into
// the machine they were taken on exactly when that machine reads their format and names a VMM
// with a version: a record never refuses itself for anything else, and never with an error
// that is not a Refusal. What it says it added again is a vCPU in a slot or refused.
func FuzzARecordReadBack(f *testing.F) {
	for _, s := range savedSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var saved Saved
		if json.Unmarshal(b, &saved) != nil {
			return
		}
		first, err := json.Marshal(saved)
		if err != nil {
			t.Fatalf("a Saved read from %q does not write: %v", b, err)
		}
		var back Saved
		if err := json.Unmarshal(first, &back); err != nil {
			t.Fatalf("a Saved written as %s does not read back: %v", first, err)
		}
		if second, _ := json.Marshal(back); string(second) != string(first) {
			t.Fatalf("a Saved read back writes another form:\n%s\n%s", first, second)
		}

		err = CanResume(saved, saved.From)
		refusal(t, err)
		_, _, versioned := vmmVersion(saved.From.VMM)
		if itself := slices.Contains(saved.From.Formats, saved.Format) && versioned; itself != (err == nil) {
			t.Fatalf("a record resumed into its own machine: %v, want refused only for its format or VMM\n%s", err, first)
		}
		if again := CanResume(back, back.From); errString(again) != errString(err) {
			t.Fatalf("read back, the record resumes into itself with %v, and before with %v", again, err)
		}

		var again added
		if len(saved.Added) == 0 || json.Unmarshal(saved.Added, &again) != nil {
			return
		}
		allowed := append([]string{"driver", "id"}, cpuTopology...)
		for _, c := range again.CPUs {
			args, err := c.deviceArgs()
			if err != nil {
				continue
			}
			for k := range maps.Keys(args) {
				if !slices.Contains(allowed, k) {
					t.Fatalf("vCPU %q is added again with %s, which names no slot", c.ID, k)
				}
			}
		}
	})
}

// Any two records that read: CanResume answers nil or a Refusal naming each difference, and nil
// only for a machine that reads the checkpoint's format.
func FuzzCanResume(f *testing.F) {
	seeds := savedSeeds(f)
	for _, s := range seeds {
		for _, h := range seeds {
			f.Add(s, h)
		}
	}
	f.Fuzz(func(t *testing.T, s, h []byte) {
		var saved, here Saved
		if json.Unmarshal(s, &saved) != nil || json.Unmarshal(h, &here) != nil {
			return
		}
		err := CanResume(saved, here.From)
		refusal(t, err)
		if err == nil && !slices.Contains(here.From.Formats, saved.Format) {
			t.Fatalf("a checkpoint of format %q resumes into a machine that reads %q", saved.Format, here.From.Formats)
		}
	})
}

// refusal fails t unless err is nil or a Refusal whose every difference names a part and a rule.
func refusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var r *Refusal
	if !errors.As(err, &r) || len(r.Differences) == 0 {
		t.Fatalf("CanResume = %v, want nil or a Refusal naming a difference", err)
	}
	for _, d := range r.Differences {
		if d.Part == "" || d.Rule == "" {
			t.Fatalf("a difference names no part or no rule: %+v", d)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
