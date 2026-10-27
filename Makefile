VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/ironpark/mako/internal/config.Version=$(VERSION)

.PHONY: build test fmt vet proto clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mako ./cmd/mako
	go build -ldflags "$(LDFLAGS)" -o bin/makod ./cmd/makod
	go build -ldflags "$(LDFLAGS)" -o bin/mako-gui ./cmd/mako-gui

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# proto lints the protocol's schema and generates its messages into
# internal/protocol; the generated code is committed, so only a schema
# change needs buf.
proto:
	buf lint
	buf generate

clean:
	rm -rf bin
