// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
)

// A NIC on passt is a network for a machine run without root and without spin: passt
// (https://passt.top) carries the guest's frames over a socket and turns them into the host's own
// TCP, UDP and ICMP sockets, so the guest reaches what the user running it reaches and nothing
// needs CAP_NET_ADMIN, a TAP or a bridge (docs/machine.md, "A network without root").
//
// Its addresses are fixed, the guest's and passt's both, so that the guest's configuration is a
// constant on its command line (passtGuest) and not whatever DHCP would hand it from the host's.
const (
	// PasstGuestAddr is the guest's address on a NIC on passt; passt translates it to the host's.
	PasstGuestAddr = "10.0.2.15"
	passtPrefix    = 24
	passtNetmask   = "255.255.255.0"
	// PasstGateway is the guest's route out. passt answers for it, and does not map it to the
	// host's loopback (--no-map-gw): a guest run by hand reaches the network, not the services
	// that listen on the host's 127.0.0.1 only.
	PasstGateway = "10.0.2.2"
	// PasstDNS is the guest's nameserver: passt forwards what is sent to it to the host's first
	// resolver (its /etc/resolv.conf), so a host whose resolver is on its loopback, as
	// systemd-resolved's 127.0.0.53 is, still answers.
	PasstDNS = "10.0.2.3"
)

// startPasst starts passt for one NIC, on one end of a socket pair whose other end it returns, for
// QEMU's stream netdev.
//
// --fd is the connected end, and implies --one-off: passt exits once QEMU's end closes, as swtpm
// does. --foreground keeps it Start's child to reap instead of a daemon nobody ends. IPv4 only, so
// that the guest's whole configuration is the one route and nameserver passtGuest gives it,
// whatever the host has. passt forwards no port to the guest unless asked, and it is not asked.
func startPasst(passt string) (*beside, *os.File, error) {
	// #nosec G204 -- the caller's passt, and constant arguments
	cmd := exec.Command(passt, "--foreground", "--fd", "3", "--ipv4-only",
		"--address", fmt.Sprintf("%s/%d", PasstGuestAddr, passtPrefix),
		"--gateway", PasstGateway, "--no-map-gw",
		"--dns-forward", PasstDNS)
	return startBeside("passt", "a NIC's socket to passt", cmd, nil)
}

// passtNetdev is the NIC's backend on passt's socket: a stream of frames on a descriptor QEMU is
// handed connected (server=off), with no vhost: frames go through QEMU and passt, as they would
// through QEMU and the TAP of a machine without /dev/vhost-net.
func passtNetdev(i, fd int) string {
	return fmt.Sprintf("stream,id=net%d,server=off,addr.type=fd,addr.str=%d", i, fd)
}

func passtNICs(nics []NIC) int {
	n := 0
	for _, nic := range nics {
		if nic.Passt != "" {
			n++
		}
	}
	return n
}

// cmdline is the kernel command line s boots with: the caller's, and for a NIC on passt the guest's
// side of its network, which passt's fixed addresses make a constant.
func (s Spec) cmdline() Cmdline {
	c := s.Cmdline
	for i, n := range s.NICs {
		if n.Passt != "" {
			c.Extra = append(passtGuest(i), c.Extra...)
		}
	}
	return c
}

// passtGuest is the guest's side of a NIC on passt, eth<i>, as two credentials on the kernel
// command line, which the image's systemd reads with nothing enabled in the image: a tmpfiles line
// for each file, the .network that gives eth<i> its address and route and /etc/resolv.conf its
// nameserver, and a udev rule that has systemd start systemd-networkd once eth<i> appears.
//
// Neither systemd-networkd nor systemd-network-generator is enabled in the image, spin's guests
// being configured by spin, and the image has no systemd-debug-generator, which is what reads
// systemd.wants= (docs/image.md, "Generators and tmpfiles rules turned off"): a device's
// SYSTEMD_WANTS is how a unit is started from the command line without it.
func passtGuest(i int) []string {
	files := fmt.Sprintf("d /run/systemd/network 0755 - - -\n"+
		"f+ /run/systemd/network/50-passt.network 0644 - - - [Match]\\nName=eth%d\\n[Network]\\nAddress=%s/%d\\nGateway=%s\\n\n"+
		"f+ /etc/resolv.conf 0644 - - - nameserver %s\\n\n",
		i, PasstGuestAddr, passtPrefix, PasstGateway, PasstDNS)
	rule := fmt.Sprintf(`SUBSYSTEM=="net", ACTION=="add", KERNEL=="eth%d", TAG+="systemd", ENV{SYSTEMD_WANTS}+="systemd-networkd.service"`+"\n", i)
	return []string{
		"systemd.set_credential_binary=tmpfiles.extra:" + base64.StdEncoding.EncodeToString([]byte(files)),
		"systemd.set_credential_binary=udev.rules.50-passt.rules:" + base64.StdEncoding.EncodeToString([]byte(rule)),
	}
}
