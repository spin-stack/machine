// SPDX-License-Identifier: Apache-2.0

// probeinit is PID 1 of the diagnostic initrd the firmware probes (boot:firmware,
// boot:firmware-stages) boot when no SPIN_PROBE_INITRD is given (boot/probe_initrd_test.go
// builds it). It is what those probes
// document an initrd's /init must do: mount proc, print the kernel log lines that mention
// vmgenid, print SPIN-READY, and stay alive printing every new kernel log line - so a restored
// guest's "crng reseeded due to virtual machine fork" reaches the host.
//
// Diagnostic input, never part of a release: what runs as a machine's PID 1 is its caller's.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func main() {
	for _, m := range []struct{ src, dir, fs string }{
		{"proc", "/proc", "proc"},
		{"devtmpfs", "/dev", "devtmpfs"},
	} {
		_ = os.MkdirAll(m.dir, 0o755)
		_ = syscall.Mount(m.src, m.dir, m.fs, 0, "")
	}

	fd, err := syscall.Open("/dev/kmsg", syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		fmt.Println("SPIN-PROBE: no /dev/kmsg:", err)
		fmt.Println("SPIN-READY")
		select {}
	}
	buf := make([]byte, 8192)
	// One record per read. What is already in the log first, without blocking, then the
	// marker, then whatever arrives after it.
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EAGAIN {
			break
		}
		if err != nil || n <= 0 {
			continue
		}
		line := message(buf[:n])
		if strings.Contains(strings.ToLower(line), "vmgenid") {
			fmt.Println(line)
		}
		// Where the kernel's clock starts, and how far along it the kernel was done: what
		// boot:firmware-stages needs to split the time after the firmware's jump.
		if strings.Contains(line, "kvm-clock: using sched offset") || strings.Contains(line, "Freeing unused kernel image") {
			fmt.Printf("SPIN-KMSG %s %s\n", stamp(buf[:n]), line)
		}
	}
	fmt.Println("SPIN-READY")
	tpm()
	_ = syscall.SetNonblock(fd, false)
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil || n <= 0 {
			continue
		}
		fmt.Println(message(buf[:n]))
	}
}

// stamp is a /dev/kmsg record's time on the kernel's clock, in µs: "6,339,5140900,-;text\n" is
// 5140900.
func stamp(rec []byte) string {
	f := strings.SplitN(string(rec), ",", 4)
	if len(f) < 4 {
		return "?"
	}
	return f[2]
}

// tpm prints what the measured boot left, after SPIN-READY so it is off every arm's clock: PCR 9
// and 11 as the kernel reads them, and the size of the firmware's event log, or that there is none.
func tpm() {
	for _, m := range []struct{ src, dir, fs string }{
		{"sysfs", "/sys", "sysfs"},
		{"securityfs", "/sys/kernel/security", "securityfs"},
	} {
		_ = os.MkdirAll(m.dir, 0o755)
		_ = syscall.Mount(m.src, m.dir, m.fs, 0, "")
	}
	pcr := func(n string) string {
		b, err := os.ReadFile("/sys/class/tpm/tpm0/pcr-sha256/" + n)
		if err != nil {
			return "none"
		}
		return strings.TrimSpace(string(b))
	}
	log := "absent"
	if b, err := os.ReadFile("/sys/kernel/security/tpm0/binary_bios_measurements"); err == nil {
		log = fmt.Sprintf("%dB", len(b))
	}
	fmt.Printf("SPIN-TPM pcr9=%s pcr11=%s eventlog=%s\n", pcr("9"), pcr("11"), log)
}

// message is a /dev/kmsg record's text: "6,339,5140900,-;text\n" is text.
func message(rec []byte) string {
	s := string(rec)
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimRight(s, "\n")
}
