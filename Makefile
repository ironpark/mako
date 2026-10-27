VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/ironpark/mako/internal/config.Version=$(VERSION)

# Nothing mako builds needs cgo (DEVELOP.md); building without it keeps the
# standard library's and the dependencies' optional cgo out too, so the
# binaries are the same wherever they are built. Override with
# `make CGO_ENABLED=1`.
CGO_ENABLED ?= 0
export CGO_ENABLED

# EXE is the binaries' suffix: .exe on Windows.
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)

.PHONY: build app test fmt vet proto clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mako$(EXE) ./cmd/mako
	go build -ldflags "$(LDFLAGS)" -o bin/makod$(EXE) ./cmd/makod
	go build -ldflags "$(LDFLAGS)" -o bin/mako-gui$(EXE) ./cmd/mako-gui

# app bundles the desktop app for macOS, signed ad hoc: posting native
# notifications takes an app with a bundle identifier.
APP := bin/mako.app

app: build
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS
	cp bin/mako-gui $(APP)/Contents/MacOS/
	sed 's/@VERSION@/$(VERSION)/' packaging/macos/Info.plist > $(APP)/Contents/Info.plist
	codesign --force --sign - $(APP)

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
