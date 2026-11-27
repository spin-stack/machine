module github.com/spin-stack/machine

go 1.27.0

toolchain go1.27.4

require (
	github.com/CycloneDX/cyclonedx-go v0.12.0
	github.com/aws/aws-sdk-go-v2 v1.48.1
	github.com/aws/aws-sdk-go-v2/config v1.33.22
	github.com/aws/aws-sdk-go-v2/service/kms v1.63.1
	github.com/google/go-tpm v0.9.8
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
	github.com/spin-stack/go-tools v0.20261127.15
	github.com/yuin/goldmark v1.8.7
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/crypto v0.59.0
	golang.org/x/sys v0.50.0
)

require (
	github.com/aws/aws-sdk-go-v2/credentials v1.21.4 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.13 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.13 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.26 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.14.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.53.1 // indirect
	github.com/aws/smithy-go v1.28.10 // indirect
	github.com/docker/cli v29.7.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/google/go-containerregistry v0.22.1 // indirect
	github.com/google/go-tpm-tools v0.3.13-0.20230620182252-4639ecce2aba // indirect
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
