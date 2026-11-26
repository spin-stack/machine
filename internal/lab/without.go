// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// hostWithout is the Spec.CPUWithout of a tree asked for as --cpu without:MODEL: the features this
// host's machine shows under host that MODEL's static expansion does not have, as the features a
// pool of this host and one of MODEL would not share (machine.Unshared, spin-stack/spin#1417).
// KVM's paravirtual features and the hypervisor bit are every host's of one QEMU and kernel, so
// Unshared never takes them away and neither does this, unless kvm is set.
//
// Checked on a second held machine that takes them away: none of them is shown there, so a tree
// whose boots would show the host's CPU all the same is refused before it is measured. desc says
// what was taken and how many features each side shows.
func hostWithout(dir, model string, kvm bool, log io.Writer) (without []string, desc string, err error) {
	var host machine.CPU
	var has []string
	err = onHeld(dir, nil, func(socket string) error {
		return askCPU(socket, func(ctx context.Context, m *machine.QEMU) error {
			var err error
			if host, err = m.CPU(ctx); err != nil {
				return err
			}
			has, err = m.ModelFeatures(ctx, model)
			return err
		})
	})
	if err != nil {
		return nil, "", err
	}
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "host shows %d features: %s\n%s has %d: %s\n", len(host.Features), strings.Join(host.Features, " "),
		model, len(has), strings.Join(has, " "))
	for _, f := range host.Features {
		if slices.Contains(has, f) || (!kvm && (strings.HasPrefix(f, "kvm") || f == "hypervisor")) {
			continue
		}
		without = append(without, f)
	}
	if len(without) == 0 {
		return nil, "", fmt.Errorf("this host shows nothing %s lacks: the tree would be host", model)
	}
	shown, still, err := showsWithout(dir, without)
	if err != nil {
		return nil, "", err
	}
	// A feature QEMU sets from the machine and not from -cpu (ht, from the topology) is shown
	// whatever -cpu says: it is the same on every host of one shape, so no pool takes it away.
	kept := still
	if len(kept) > 0 {
		without = slices.DeleteFunc(without, func(f string) bool { return slices.Contains(kept, f) })
		if shown, still, err = showsWithout(dir, without); err != nil {
			return nil, "", err
		}
		if len(still) > 0 {
			return nil, "", fmt.Errorf("a machine told to take away %s still shows %s", strings.Join(without, ","), strings.Join(still, ","))
		}
	}
	return without, fmt.Sprintf("host %s without the %d features %s lacks (QEMU kept %s): shows %d of host's %d",
		host.Properties["model-id"], len(without), model, cmp.Or(strings.Join(kept, ","), "none"), len(shown.Features), len(host.Features)), nil
}

// showsWithout is the CPU a held machine told to take away without shows, and which of without it
// still shows.
func showsWithout(dir string, without []string) (shown machine.CPU, still []string, err error) {
	err = onHeld(dir, []string{"--cpu-without=" + strings.Join(without, ",")}, func(socket string) error {
		return askCPU(socket, func(ctx context.Context, m *machine.QEMU) error {
			var err error
			shown, err = m.CPU(ctx)
			return err
		})
	})
	for _, f := range without {
		if slices.Contains(shown.Features, f) {
			still = append(still, f)
		}
	}
	return shown, still, err
}

func askCPU(socket string, ask func(context.Context, *machine.QEMU) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := machine.Dial(ctx, socket, qmp.Observer{})
	if err != nil {
		return fmt.Errorf("asking about this host's CPU: %w", err)
	}
	defer func() { _ = m.Close() }()
	return ask(ctx, m)
}
