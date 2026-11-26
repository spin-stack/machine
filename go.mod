module github.com/spin-stack/machine

go 1.27.0

toolchain go1.27.3

require (
	github.com/CycloneDX/cyclonedx-go v0.12.0
	github.com/google/go-tpm v0.9.8
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
	github.com/spin-stack/go-tools v0.20261125.1
	github.com/yuin/goldmark v1.8.7
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/crypto v0.59.0
	golang.org/x/sys v0.50.0
)

require (
	github.com/docker/cli v29.7.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/google/go-containerregistry v0.22.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	golang.org/x/sync v0.24.0 // indirect
)

tool (
	github.com/spin-stack/go-tools/cmd/ghatrace
	github.com/spin-stack/go-tools/cmd/task
	github.com/spin-stack/go-tools/cmd/versions
)
