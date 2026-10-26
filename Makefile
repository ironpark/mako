VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/ironpark/sanbo/internal/config.Version=$(VERSION)

.PHONY: build test fmt vet proto clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/sanbo ./cmd/sanbo
	go build -ldflags "$(LDFLAGS)" -o bin/sanbod ./cmd/sanbod
	go build -ldflags "$(LDFLAGS)" -o bin/sanbo-gui ./cmd/sanbo-gui

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# proto lints the protocol's schema and generates internal/protocol/pb from
# it; the generated code is committed, so only a schema change needs buf.
proto:
	buf lint
	buf generate

clean:
	rm -rf bin
