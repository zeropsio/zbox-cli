module github.com/zeropsio/zbox-cli

go 1.24.0

toolchain go1.24.2

require (
	github.com/gorilla/websocket v1.5.3
	github.com/spf13/cobra v1.9.1
	github.com/zeropsio/zerops-go v0.0.0
	golang.org/x/term v0.35.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/shopspring/decimal v1.3.1 // indirect
	github.com/spf13/pflag v1.0.6 // indirect
	golang.org/x/sys v0.36.0 // indirect
)

replace github.com/zeropsio/zerops-go => ../nest/pkg/zerops/publicSdk/zerops-go
