// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// cmdline is the kernel command line s boots with: the caller's, for a NIC on passt the guest's
// side of its network, which passt's fixed addresses make a constant, and for a TPM
// systemd.tpm2_measured_os=1. systemd measures its boot phases into PCR 11 only on a measured OS,
// which without the parameter means one booted through systemd-stub, and this machine boots no
// UKI (#773, docs/machine.md, "A TPM").
func (s Spec) cmdline() Cmdline {
	c := s.Cmdline
	for i, n := range s.NICs {
		if n.Passt != "" {
			c.Extra = append(passtGuest(i), c.Extra...)
		}
	}
	if s.TPM.State != "" {
		c.Extra = append([]string{"systemd.tpm2_measured_os=1"}, c.Extra...)
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

// PasstProfileName is the AppArmor profile PasstAppArmor writes, and the file under
// /etc/apparmor.d it is installed as. One name for every release: installing a release's profile
// replaces the one before, which a host that keeps two releases installs again when it boots the
// other's.
const PasstProfileName = "spin-machine-passt"

// PasstAppArmor is the AppArmor profile that lets passt, the release's, make the user namespace it
// sandboxes itself in on a host whose AppArmor refuses one to an unprivileged process
// (kernel.apparmor_restrict_unprivileged_userns, as on the CI runners' Ubuntu 26.04 image, run
// 45798835676): passt's own profile, confined, attached to that one file (docs/machine.md, "A
// network without root").
//
// The attachment is passt's path with its symlinks resolved, which is the path AppArmor matches
// at exec, and never a pattern: a profile for any file named passt would give a user namespace
// to whatever a user names so, which is what the restriction refuses. A path AppArmor would read
// as a pattern or a variable is refused for the same reason, and so is any character it might
// not read literally inside quotes.
func PasstAppArmor(passt string) (string, error) {
	p, err := filepath.Abs(passt)
	if err == nil {
		p, err = filepath.EvalSymlinks(p)
	}
	if err != nil {
		return "", fmt.Errorf("resolving passt's path, which its profile attaches to: %w", err)
	}
	if c := notLiteralInProfile.FindString(p); c != "" {
		return "", fmt.Errorf("passt is at %q, whose %q a profile may not read as itself: the profile attaches to the one file, so unpack the release at a path of ASCII letters, digits and ._+-/", p, c)
	}
	return fmt.Sprintf(passtProfile, p, PasstProfileName, p), nil
}

// notLiteralInProfile is a character of a path that a profile might not read as itself: a glob's
// (*?[]{}^), a variable's (@), a quote's or an escape's, space, and anything past ASCII.
var notLiteralInProfile = regexp.MustCompile(`[^A-Za-z0-9._+/-]`)

// passtProfile is passt's own AppArmor profile, contrib/apparmor/usr.bin.passt and the
// abstractions/passt it includes at the passt versions.yaml pins, made one file so a host needs
// nothing of passt's installed, and narrowed to passt as startPasst runs it:
//
//   - The attachment is the one file, not /usr/bin/passt{,.avx2}.
//   - No abstractions/user-tmp and no owner @{HOME}/** w: those are for a pcap, a PID file or a log
//     file, and startPasst asks for none.
//   - No libvirt's run directories, nor /usr/bin/passt r, which upstream carries for libvirt.
//   - No passt.avx2 ix: the release has no passt.avx2 (qemu/Dockerfile), and passt's exec of one
//     planted beside it is refused instead of run under this profile.
//
// Everything else is upstream's, rule for rule: userns, and the capabilities and mounts passt
// sandboxes itself with inside it.
const passtProfile = `# AppArmor profile for the passt of a spin-machine release, at %s:
# passt's own (contrib/apparmor at the version the release pins), narrowed to passt as
# spin-machine runs it. Written by spin-machine apparmor.

abi <abi/4.0>,

include <tunables/global>

profile %s "%s" flags=(attach_disconnected) {
  include <abstractions/base>
  include <abstractions/nameservice>

  capability net_bind_service,
  capability setuid,
  capability setgid,
  capability sys_admin,
  capability setpcap,
  capability net_admin,
  capability sys_ptrace,
  capability setfcap,
  userns,

  / r,
  mount options=(rw, runbindable) -> /,
  mount "" -> "/",
  mount "" -> "/tmp/",
  pivot_root "/tmp/" -> "/tmp/",
  umount "/",

  owner @{PROC}/@{pid}/gid_map w,
  owner @{PROC}/@{pid}/setgroups w,
  owner @{PROC}/@{pid}/uid_map rw,

  @{PROC}/sys/net/ipv4/ip_local_port_range r,
  @{PROC}/sys/net/ipv4/tcp_syn_retries r,
  @{PROC}/sys/net/ipv4/tcp_syn_linear_timeouts r,
  @{PROC}/sys/net/ipv4/tcp_rto_max_ms r,
  @{PROC}/sys/net/netfilter/nf_conntrack_udp_timeout r,
  @{PROC}/sys/net/netfilter/nf_conntrack_udp_timeout_stream r,

  network netlink raw,
  network inet stream,
  network inet6 stream,
  network inet dgram,
  network inet6 dgram,
  network unix stream,
  network unix dgram,
}
`
