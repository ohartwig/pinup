module git.acme.test/pinup/synthetic

go 1.27.0

toolchain go1.27.1

require (
	go.etcd.io/bbolt v1.5.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

require golang.org/x/mobile v0.0.0-20260821190718-4776eadac327

require github.com/aws/aws-sdk-go-v2 v1.43.8

replace github.com/aws/aws-sdk-go-v2 => github.com/aws/aws-sdk-go-v2 v1.43.7

exclude golang.org/x/text v0.3.0

tool golang.org/x/tools/cmd/stringer
