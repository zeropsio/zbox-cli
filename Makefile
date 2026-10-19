MODULE  := github.com/zeropsio/zbox-cli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/cmd.version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build install test vet generate release clean

build:
	go build -ldflags "$(LDFLAGS)" -o zbox ./cmd/zbox

install:
	go build -ldflags "$(LDFLAGS)" -o ~/.local/bin/zbox ./cmd/zbox

test:
	go test ./...

vet:
	go vet ./...

generate:
	go generate ./internal/api

# Cross-compiles every target into dist/, what the release workflow does per matrix job.
release:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$t"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/zbox-$$os-$$arch$$ext ./cmd/zbox || exit 1; \
	done

clean:
	rm -rf dist zbox
