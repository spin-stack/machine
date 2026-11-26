# SPDX-License-Identifier: Apache-2.0
# The operator half of shirudo a release ships (shirudo/Dockerfile, `operator`), read after
# shirudo's own Makefile.shirudo with its target profile: its lists and its objects, never copies
# of them here.
#
# shirudo-build is linked as shirudo links shirudo-target, a static PIE of the target's objects
# and the dependencies Makefile.target-deps built, so it runs in a guest of the release with
# nothing installed beside it. shirudo's own rule links it against the system's shared libraries.
#
# The tree is laid out as shirudo's install-operator lays out /usr/local, which is what
# guest/shirudo looks under for shirudo-build and shirudo-build for its clang, headers and
# vmlinux.h; the operator's install links those paths to this tree where it is unpacked.

OPERATOR ?= /out/shirudo-operator

tools/shirudo-build/shirudo-build: HARDEN_LDFLAGS := $(HARDEN_STATIC_PIE_LDFLAGS)
tools/shirudo-build/shirudo-build: LDFLAGS += -static-pie -static-libgcc
tools/shirudo-build/shirudo-build: DYNAMIC_LIBS = $(TARGET_TLS_STATIC_LINK_LIBS) $(TARGET_STATIC_RUNTIME_LINK_LIBS)

.PHONY: operator
operator: tools/shirudo-build/shirudo-build $(VMLINUX_H) $(BPF_LIBBPF_HEADERS) $(BPF_LIB_HEADERS)
	sh .github/scripts/check-static-target.sh tools/shirudo-build/shirudo-build
	install -d $(OPERATOR)/libexec/shirudo/gen-image $(OPERATOR)/libexec/shirudo/gen-initrd \
		$(OPERATOR)/share/shirudo/bpf/include/bpf $(OPERATOR)/share/shirudo/systemd
	llvm-strip -o $(OPERATOR)/libexec/shirudo/shirudo-build tools/shirudo-build/shirudo-build
	install -m 0755 tools/gen-image/shirudo-gen-* $(OPERATOR)/libexec/shirudo/gen-image/
	install -m 0644 tools/gen-image/common.sh tools/gen-image/runtime-service*.conf.in \
		$(OPERATOR)/libexec/shirudo/gen-image/
	install -m 0755 tools/gen-initrd/shirudo-gen-initrd tools/gen-initrd/module-setup.sh \
		$(OPERATOR)/libexec/shirudo/gen-initrd/
	install -m 0644 $(VMLINUX_H) $(OPERATOR)/share/shirudo/vmlinux.h
	install -m 0644 $(BPF_LIBBPF_HEADERS) $(OPERATOR)/share/shirudo/bpf/include/bpf/
	install -m 0644 $(BPF_LIB_HEADERS) $(OPERATOR)/share/shirudo/bpf/include/
	install -m 0644 systemd/*.in systemd/*.timer systemd/*.rules $(OPERATOR)/share/shirudo/systemd/
	cp -R upatches $(OPERATOR)/share/shirudo/upatches
