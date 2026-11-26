# docs/image.md, "The rebuilt packages"
TPM2_FLAGS = -Dtpm2=enabled -Dopenssl=enabled -Dbootloader=enabled -Dshared-lib-tag=tpm2
TPM2_FILES = -path '*pcr*' -not -name cryptsetup.nvpcr -o -path '*tpm2*' -not -name 60-tpm2-id.rules -o -name systemd-measure -o -name systemd-analyze

execute_after_dh_auto_install:
	dh_auto_configure --builddirectory=tpm2-build -- $(CONFFLAGS) $(CONFFLAGS_DISTRO) $(CONFFLAGS_UPSTREAM) $(TPM2_FLAGS)
	dh_auto_build --builddirectory=tpm2-build
	dh_auto_install --builddirectory=tpm2-build --destdir=debian/tpm2
	cd debian/tpm2 && find . \( $(TPM2_FILES) \) -not -path '*/sysinit.target.wants/*' -not -path '*/sockets.target.wants/*' -not -type d -print0 | xargs -0 cp -a --parents -t ../tmp
