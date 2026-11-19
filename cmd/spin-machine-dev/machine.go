// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/spin-machine/internal/build"
)

func init() {
	verbs = append(verbs, shellCmd, vsockCmd, menuconfigCmd, cleanCmd)
}

func shellCmd() *cobra.Command {
	var init string
	var memory, cpus int
	c := &cobra.Command{
		Use:   "shell",
		Short: "Boot this machine and get a shell in the base image, to see how it feels and what it is missing",
		Long: `Boot this machine from _output and get a console in the base image: systemd, logged in as root,
because that is what the image is - a bare shell answers a different question, and systemd-analyze in
it says "System has not been booted with systemd as init system". --init /bin/bash is there for when
systemd is the thing that is broken.

Booted through _output/bin/spin-machine, whose command line is the machine package's: a slot map or
a kernel argument that stopped working stops working here first. With no --disk the base boots
under a throwaway overlay (QEMU's -snapshot), so every write is gone when it ends, and the base -
which every VM maps read-only, many at once - is hashed before and after: a session that changed it
fails. accel=kvm with no fallback: a host with no /dev/kvm gets an error, not a guest at a tenth of
the speed. Leave with 'poweroff -f', or Ctrl-A then X.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			base := filepath.Join(w.out, "image", "rootfs.qcow2")
			for _, f := range []string{"bin/spin-machine", "bin/qemu-system-x86_64", "kernel/bzImage", "image/rootfs.qcow2"} {
				if _, err := os.Stat(filepath.Join(w.out, f)); err != nil {
					return couldNot(fmt.Errorf("missing %s: spin-machine-dev build makes it", filepath.Join(w.out, f)))
				}
			}
			before, err := build.FileSHA256(base)
			if err != nil {
				return couldNot(err)
			}
			say(c, "booting %s in the base image — %d MiB, %d vCPU", init, memory, cpus)
			say(c, "  writes go to a throwaway overlay; the base is read-only and checked after")
			say(c, "  leave with: 'poweroff -f', or Ctrl-A then X")
			boot := exec.CommandContext(c.Context(), filepath.Join(w.out, "bin", "spin-machine"), "boot", //nolint:gosec // this build's spin-machine
				"--release", w.out, "--memory", fmt.Sprint(memory), "--cpus", fmt.Sprint(cpus), "--init", init)
			boot.Stdin, boot.Stdout, boot.Stderr = os.Stdin, os.Stdout, os.Stderr
			_ = boot.Run() // the guest's own exit is the session ending, however it ended
			after, err := build.FileSHA256(base)
			if err != nil {
				return couldNot(err)
			}
			if before != after {
				return exitError{code: 1, err: fmt.Errorf("the base image changed during that session: %s -> %s; every overlay in existence is built on a file that moved", before, after)}
			}
			say(c, "rootfs.qcow2 unchanged; the overlay's writes are gone")
			return nil
		},
	}
	c.Flags().StringVar(&init, "init", "/sbin/init", "the guest's first process")
	c.Flags().IntVar(&memory, "memory", 2048, "the guest's memory, in MiB")
	c.Flags().IntVar(&cpus, "cpus", 2, "the guest's vCPUs")
	return c
}

func vsockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "vsock",
		Short: "Make /dev/vhost-vsock usable by this user, so the machine's vsock device can be created",
		Long: `Make /dev/vhost-vsock usable by this user, so verify args can create the machine's vsock device.
Without it, says so rather than failing: verify args names the device as unchecked.

The module before the node is looked at: GitHub's runner has the static node before the module is
loaded, the first open loads it, and udev then sets the node back to root's - QEMU got "Permission
denied" after a chmod that saw the node and skipped the modprobe (2026-10-24). A kernel that builds
it in has the device from boot, and modprobe there finds nothing. Needs sudo.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_ = exec.CommandContext(c.Context(), "sudo", "modprobe", "vhost_vsock").Run()
			_ = exec.CommandContext(c.Context(), "sudo", "udevadm", "settle").Run()
			if fi, err := os.Stat("/dev/vhost-vsock"); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
				say(c, "no vhost_vsock here; verify args will name vhost-vsock-pci as unchecked")
				return nil
			}
			if out, err := exec.CommandContext(c.Context(), "sudo", "chmod", "0666", "/dev/vhost-vsock").CombinedOutput(); err != nil {
				return couldNot(fmt.Errorf("making /dev/vhost-vsock usable: %w: %s", err, strings.TrimSpace(string(out))))
			}
			return nil
		},
	}
}

func menuconfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "menuconfig",
		Short: "Edit the kernel's configuration, written back to kernel/config-<series>-<arch>",
		Long: `Edit the kernel's configuration in menuconfig, in the kernel's build environment, written back
to kernel/config-<series>-<arch>.

KCONFIG_CONFIG is the whole mechanism: kconfig reads and writes that path, so menuconfig opens the
repository's file and saves straight onto it. There is no copy of .config afterwards and there must
not be: one copied the container's olddefconfig output over what menuconfig had just written,
discarding every edit, and exited 0 doing it.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			series, err := build.KernelSeries(w.pins)
			if err != nil {
				return couldNot(err)
			}
			kernel, err := build.Lookup("kernel")
			if err != nil {
				return couldNot(err) // mutate-exempt: kernel is a part of build.Parts, which build's tests make
			}
			args, err := build.Args(w.pins, kernel.Pins...)
			if err != nil {
				return couldNot(err)
			}
			image := "spin-machine-menuconfig"
			bx := []string{"buildx", "build", "--file", "kernel/Dockerfile", "--target", "kernel-build-base", "--platform", "linux/amd64",
				"--cache-from", "type=local,src=" + filepath.Join(w.root, ".cache", "buildkit", "kernel")}
			bx = append(bx, args...)
			bx = append(bx, "--build-arg", "KERNEL_SERIES="+series, "--build-arg", "KERNEL_ARCH="+build.Arch,
				"--output", "type=docker,name="+image, ".")
			if err := command(c.Context(), c, w.root, "docker", bx...); err != nil {
				return err
			}
			term := os.Getenv("TERM")
			if term == "" {
				term = "xterm-256color"
			}
			return command(c.Context(), c, w.root, "docker", "run", "--rm", "-it",
				"-v", filepath.Join(w.root, "kernel")+":/config", "-w", "/usr/src/linux",
				"-e", "KCONFIG_CONFIG=/config/config-"+series+"-"+build.Arch, "-e", "TERM="+term,
				image, "make", "ARCH="+build.Arch, "menuconfig")
		},
	}
}

func cleanCmd() *cobra.Command {
	var cache bool
	c := &cobra.Command{
		Use:   "clean",
		Short: "Remove _output; --cache also the BuildKit and mkosi caches, after which a full build is hours",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			gone := []string{w.out}
			if cache {
				gone = append(gone, filepath.Join(w.root, ".cache", "buildkit"), filepath.Join(w.root, ".cache", "mkosi"))
			}
			var errs []error
			for _, d := range gone {
				if err := os.RemoveAll(d); err != nil {
					errs = append(errs, err)
				}
			}
			if err := errors.Join(errs...); err != nil {
				return couldNot(err)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&cache, "cache", false, "remove the BuildKit and mkosi caches too")
	return c
}
