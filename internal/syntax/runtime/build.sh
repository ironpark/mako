#!/bin/sh
# Builds tree-sitter.wasm: the tree-sitter C runtime plus shim.c, compiled
# to a wasm32-wasi reactor that grammar modules link against at load time.
#
# Needs curl, tar, shasum (or sha256sum) and zig (tested with 0.17) on PATH.
set -eu

TS_VERSION=0.26.13
TS_SHA256=ece24c3c5e2a76384075e830c7139b59fce8fb01e4ef8436fab08bbe10444c89

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -sSLf -o "$work/ts.tar.gz" \
  "https://github.com/tree-sitter/tree-sitter/archive/refs/tags/v$TS_VERSION.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  sum=$(sha256sum "$work/ts.tar.gz" | cut -d' ' -f1)
else
  sum=$(shasum -a 256 "$work/ts.tar.gz" | cut -d' ' -f1)
fi
if [ "$sum" != "$TS_SHA256" ]; then
  echo "tree-sitter tarball checksum mismatch: $sum" >&2
  exit 1
fi
mkdir "$work/ts"
tar -xzf "$work/ts.tar.gz" -C "$work/ts" --strip-components=1

# libc functions grammars may import: exported by name, and listed in
# libc_fns.h so their table indices are known.
syms=$(grep -v '^#' "$here/libc-symbols.txt" | grep -v '^$')
exports=""
for s in $syms; do
  echo "(void *)$s," >> "$work/libc_fns.h"
  exports="$exports -Wl,--export=$s"
done

# shellcheck disable=SC2086
zig cc -target wasm32-wasi -mexec-model=reactor -O2 -std=c11 \
  -D_DEFAULT_SOURCE -DNDEBUG \
  -I "$work/ts/lib/include" -I "$work/ts/lib/src" -I "$work" \
  "$work/ts/lib/src/lib.c" "$here/shim.c" \
  -Wl,--export-table -Wl,--growable-table $exports \
  -Wl,-z,stack-size=1048576 -Wl,--strip-all \
  -o "$here/tree-sitter.wasm"

echo "$TS_VERSION" > "$here/VERSION"
