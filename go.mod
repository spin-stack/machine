module github.com/spin-stack/spin-machine

go 1.27.0

toolchain go1.27.3

require (
	github.com/spin-stack/go-tools v0.20261118.5
	golang.org/x/crypto v0.59.0
	golang.org/x/sys v0.50.0
)

require go.yaml.in/yaml/v3 v3.0.6 // indirect

tool github.com/spin-stack/go-tools/cmd/task
