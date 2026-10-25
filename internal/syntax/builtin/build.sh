#!/bin/sh
# Builds the builtin grammar pack: each pinned tree-sitter grammar compiled
# to a wasm side module in the format Zed ships (dylink.0, PIC, memory,
# table, globals and libc imported from env), gzipped, with the grammar
# repo's own queries and LICENSE beside it under grammars/<name>/.
#
# Needs curl, tar, gzip, shasum (or sha256sum) and zig (tested with 0.17).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# fetch <id> <github repo> <tag> <sha256>: unpack a release into $work/<id>.
fetch() {
  curl -sSLf -o "$work/$1.tar.gz" "https://github.com/$2/archive/refs/tags/$3.tar.gz"
  sum=$(sha256 "$work/$1.tar.gz")
  if [ "$sum" != "$4" ]; then
    echo "$2 $3: checksum mismatch: $sum" >&2
    exit 1
  fi
  mkdir "$work/$1"
  tar -xzf "$work/$1.tar.gz" -C "$work/$1" --strip-components=1
}

fetch go         tree-sitter/tree-sitter-go               v0.25.0 2dc241b97872c53195e01b86542b411a3c1a6201d9c946c78d5c60c063bba1ef
fetch rust       tree-sitter/tree-sitter-rust             v0.24.2 061e90a539a55a6aa65dceb0ad6425c50ab1a6e3e6d4ba430e2795ed4550f10e
fetch python     tree-sitter/tree-sitter-python           v0.25.0 4609a3665a620e117acf795ff01b9e965880f81745f287a16336f4ca86cf270c
fetch javascript tree-sitter/tree-sitter-javascript       v0.25.0 9712fc283d3dc01d996d20b6392143445d05867a7aad76fdd723824468428b86
fetch typescript tree-sitter/tree-sitter-typescript       v0.23.2 2c4ce711ae8d1218a3b2f899189298159d672870b5b34dff5d937bed2f3e8983
fetch json       tree-sitter/tree-sitter-json             v0.24.8 acf6e8362457e819ed8b613f2ad9a0e1b621a77556c296f3abea58f7880a9213
fetch bash       tree-sitter/tree-sitter-bash             v0.25.1 2e785a761225b6c433410ef9c7b63cfb0a4e83a35a19e0f2aec140b42c06b52d
fetch css        tree-sitter/tree-sitter-css              v0.25.0 03965344d8c0435dc54fb45b281578420bb7db8b99df4d34e7e74105a274cb79
fetch yaml       tree-sitter-grammars/tree-sitter-yaml    v0.7.2  aeaff5731bb8b66c7054c8aed33cd5edea5f4cd2ac71654f3f6c2ba2073d8fac
fetch c          tree-sitter/tree-sitter-c                v0.24.2 2eeb4db31f8fa0865e45488503d13403923bcb485a1bdb637abff8c42dd97364
fetch cpp        tree-sitter/tree-sitter-cpp              v0.23.4 7a2c55afe3028f4105f25762ea58cc16537d1f5a1dcd9cca90410b3cd5d46051
fetch markdown   tree-sitter-grammars/tree-sitter-markdown v0.5.4 481ce2c1cce7b1130bc8455c186052bd0fa461f1ff488666994c6a3da7fca2ac
fetch diff       tree-sitter-grammars/tree-sitter-diff    v0.2.0  6092f7fd11e186d96d3c130fdbe8d03cfc426ee9cc62cf4a2112571b1b0372e6
fetch dockerfile camdencheek/tree-sitter-dockerfile       v0.2.0  8cbdf50838cc55e841aa9585a1a09e5b8c41454ae30ce0a93a7bd71adb140818
fetch java       tree-sitter/tree-sitter-java             v0.23.5 cb199e0faae4b2c08425f88cbb51c1a9319612e7b96315a174a624db9bf3d9f0
fetch ruby       tree-sitter/tree-sitter-ruby             v0.23.1 e7e49577ddc1f2de8e42d42353b477e338c15bbb95b2558e123ddc13d88789f0
fetch php        tree-sitter/tree-sitter-php              v0.25.1 1377ac7d40474e002af12fe098287e065fac6d818d43376dc6617dbc94836ad8

# grammar <name> <src dir> <highlights> <injections> <licenses>
# Query and license lists are space-separated <archive>:<path> entries;
# highlights are concatenated base language first, as Neovim and Helix do,
# since later patterns take precedence. (tree-sitter.json lists
# typescript's own queries before javascript's, an order from when
# earlier patterns won.)
grammar() {
  name=$1 src=$work/$2 out=$here/grammars/$1
  rm -rf "$out"
  mkdir -p "$out"
  objs=""
  for f in parser scanner; do
    [ -f "$src/$f.c" ] || continue
    zig cc -target wasm32-wasi -fPIC -Os -std=c11 -fvisibility=hidden -mno-extended-const \
      -I "$src" -c "$src/$f.c" -o "$work/$name-$f.o"
    objs="$objs $work/$name-$f.o"
  done
  # wazero lacks extended-const, which PIC data offsets would otherwise
  # use. Undefined libc symbols become env imports, resolved by the runtime.
  # shellcheck disable=SC2086
  zig wasm-ld -shared --experimental-pic --no-entry \
    --unresolved-symbols=import-dynamic --export="tree_sitter_$name" \
    --strip-debug $objs -o "$work/$name.wasm"
  gzip -9 -n -c "$work/$name.wasm" > "$out/$name.wasm.gz"

  concat "$3" "$out/highlights.scm"
  [ -z "$4" ] || concat "$4" "$out/injections.scm"
  for l in $5; do
    if [ "$l" = "${5%% *}" ]; then
      cp "$work/${l%%:*}/${l#*:}" "$out/LICENSE"
    else
      cp "$work/${l%%:*}/${l#*:}" "$out/LICENSE.${l%%:*}"
    fi
  done
}

concat() {
  : > "$2"
  for q in $1; do
    printf '; From %s/%s\n\n' "${q%%:*}" "${q#*:}" >> "$2"
    cat "$work/${q%%:*}/${q#*:}" >> "$2"
    printf '\n' >> "$2"
  done
}

grammar go go/src "go:queries/highlights.scm" "" "go:LICENSE"
grammar rust rust/src "rust:queries/highlights.scm" "rust:queries/injections.scm" "rust:LICENSE"
grammar python python/src "python:queries/highlights.scm" "" "python:LICENSE"
grammar javascript javascript/src \
  "javascript:queries/highlights.scm javascript:queries/highlights-jsx.scm javascript:queries/highlights-params.scm" \
  "javascript:queries/injections.scm" "javascript:LICENSE"
grammar typescript typescript/typescript/src \
  "javascript:queries/highlights.scm typescript:queries/highlights.scm" \
  "javascript:queries/injections.scm" "typescript:LICENSE javascript:LICENSE"
grammar tsx typescript/tsx/src \
  "javascript:queries/highlights.scm javascript:queries/highlights-jsx.scm typescript:queries/highlights.scm" \
  "javascript:queries/injections.scm" "typescript:LICENSE javascript:LICENSE"
grammar json json/src "json:queries/highlights.scm" "" "json:LICENSE"
grammar bash bash/src "bash:queries/highlights.scm" "" "bash:LICENSE"
grammar css css/src "css:queries/highlights.scm" "" "css:LICENSE"
grammar yaml yaml/src "yaml:queries/highlights.scm" "" "yaml:LICENSE"
grammar c c/src "c:queries/highlights.scm" "" "c:LICENSE"
grammar cpp cpp/src "c:queries/highlights.scm cpp:queries/highlights.scm" \
  "cpp:queries/injections.scm" "cpp:LICENSE c:LICENSE"
grammar markdown markdown/tree-sitter-markdown/src \
  "markdown:tree-sitter-markdown/queries/highlights.scm" \
  "markdown:tree-sitter-markdown/queries/injections.scm" "markdown:LICENSE"
grammar markdown_inline markdown/tree-sitter-markdown-inline/src \
  "markdown:tree-sitter-markdown-inline/queries/highlights.scm" \
  "markdown:tree-sitter-markdown-inline/queries/injections.scm" "markdown:LICENSE"
grammar diff diff/src "diff:queries/highlights.scm" "diff:queries/injections.scm" "diff:LICENSE"
grammar dockerfile dockerfile/src "dockerfile:queries/highlights.scm" "" "dockerfile:LICENSE"
grammar java java/src "java:queries/highlights.scm" "" "java:LICENSE"
grammar ruby ruby/src "ruby:queries/highlights.scm" "" "ruby:LICENSE"
grammar php php/php/src "php:queries/highlights.scm" \
  "php:queries/injections.scm php:queries/injections-text.scm" "php:LICENSE"
