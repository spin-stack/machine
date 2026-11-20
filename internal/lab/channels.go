// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"strconv"
	"strings"
)

// channelsOption compares how many threads move a checkpoint's memory (spin-machine save and
// restore --channels, QEMU's multifd): each count is the probe's restore rows over again, and one
// measurement saves and restores with the same count, as a host that chose it would.
var channelsOption = Option{"channels", "", "spin-machine save and restore --channels to compare, space-separated, each the restore rows over again (e.g. 1 2 4 8); none is the rows as the tree's spin-machine runs them"}

// withChannels is rows once for each count o's channels names, each named for its count, or rows
// as they are when it names none.
func withChannels(o Options, rows ...string) []string {
	counts := o.Fields(channelsOption.Name)
	if len(counts) == 0 {
		return rows
	}
	var out []string
	for _, n := range counts {
		for _, r := range rows {
			out = append(out, r+", channels "+n)
		}
	}
	return out
}

// channelsNeeds are the flags a probe comparing channels runs, when it does.
func channelsNeeds(o Options) []Need {
	if len(o.Fields(channelsOption.Name)) == 0 {
		return nil
	}
	return []Need{{"save", "channels"}, {"restore", "channels"}}
}

// rowChannels splits a row withChannels named into the row it repeats and spin-machine's
// --channels flag for it: none for a row named for no count.
func rowChannels(row string) (string, []string, error) {
	way, count, ok := strings.Cut(row, ", ")
	if !ok {
		return row, nil, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(count, "channels "))
	if err != nil || n < 1 {
		return "", nil, fmt.Errorf("row %q: %q is not a number of channels", row, count)
	}
	return way, []string{"--channels", strconv.Itoa(n)}, nil
}
