// SPDX-License-Identifier: Apache-2.0

// keyring-probe says of the .bpf and .dm-verity keyrings whether each is sealed, which nothing in
// the image can ask: the kernel shows no restriction in /proc/keys, and the image has no keyctl. A
// sealed keyring refuses every key with EPERM (security/keys/keyring.c, restrict_link_reject), so
// the probe adds one to each and takes it out again. It prints one line per keyring: its name and
// sealed or open.
package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const keyctlUnlink = 9

func main() {
	for _, k := range []struct {
		name string
		id   int32
	}{{".bpf", -9}, {".dm-verity", -10}} {
		fmt.Printf("%s %s\n", k.name, probe(k.id))
	}
}

func probe(keyring int32) string {
	typ, desc := cstr("user"), cstr("spin-keyring-probe")
	payload := []byte("x")
	id, _, errno := syscall.Syscall6(syscall.SYS_ADD_KEY, uintptr(unsafe.Pointer(typ)), uintptr(unsafe.Pointer(desc)),
		uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), uintptr(keyring), 0)
	switch {
	case errno == 0:
		if _, _, errno := syscall.Syscall(syscall.SYS_KEYCTL, keyctlUnlink, id, uintptr(keyring)); errno != 0 {
			fmt.Fprintf(os.Stderr, "taking the probe's key out of keyring %d: %v\n", keyring, errno)
			os.Exit(1)
		}
		return "open"
	case errors.Is(errno, syscall.EPERM):
		return "sealed"
	}
	fmt.Fprintf(os.Stderr, "adding a key to keyring %d: %v\n", keyring, errno)
	os.Exit(1)
	return ""
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}
