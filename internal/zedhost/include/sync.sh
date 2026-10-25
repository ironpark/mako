#!/bin/sh
# Copies the C headers of the libwasmtime that wasmtime-go bundles, so the
# component bindings compile against the library they link. Run it after
# moving wasmtime-go to another version.
set -eu
cd "$(dirname "$0")"
mod=$(go list -m -f '{{.Dir}}' github.com/bytecodealliance/wasmtime-go/v50)
find . -name '*.h' -delete
(cd "$mod/build/include" && find . -name '*.h' ! -name 'doc-wasm.h') | while read -r f; do
	mkdir -p "$(dirname "$f")"
	cp "$mod/build/include/$f" "$f"
	chmod 644 "$f"
done
go list -m -f '{{.Version}}' github.com/bytecodealliance/wasmtime-go/v50 > VERSION
