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

.PHONY: build app dev test fmt vet proto clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mako$(EXE) ./cmd/mako
	go build -ldflags "$(LDFLAGS)" -o bin/makod$(EXE) ./cmd/makod
	go build -ldflags "$(LDFLAGS)" -o bin/mako-gui$(EXE) ./cmd/mako-gui

# app bundles the desktop app for macOS with the ggui command
# (`go install github.com/ironpark/ggui/cmd/ggui@latest`), from ggui.toml,
# signed ad hoc: posting native notifications takes an app with a bundle
# identifier. The bundle's version is the latest tag's X.Y.Z, which
# Info.plist needs plain; the program is stamped with $(VERSION) as `build`'s are.
APP_VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | grep -oE '[0-9]+(\.[0-9]+){0,2}' | head -n 1)

app:
	@mkdir -p bin
	sed -E '$(if $(APP_VERSION),s/^version = ".*"/version = "$(APP_VERSION)"/)' ggui.toml > bin/ggui.toml
	ggui build --target macos --config bin/ggui.toml --out bin --ldflags "$(LDFLAGS)" ./cmd/mako-gui

# dev runs the desktop app with the ggui command, rebuilding and restarting
# it whenever its code changes.
dev:
	ggui dev ./cmd/mako-gui

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# proto lints the protocol's schema and generates its messages into
# internal/protocol, and the Android app's; the generated code is
# committed, so only a schema change needs buf.
proto:
	buf lint
	buf generate
	buf generate --template app/android/buf.gen.yaml

clean:
	rm -rf bin
