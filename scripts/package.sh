#!/bin/sh
# Builds the release archives that src/lib.rs downloads, into ./dist.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/dist"
rm -rf "$out"
mkdir -p "$out"

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  exe=go-coverage-lsp
  [ "$os" = windows ] && exe=go-coverage-lsp.exe

  stage=$(mktemp -d)
  (cd "$root/server" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    go build -trimpath -ldflags "-s -w" -o "$stage/$exe" ./cmd/go-coverage-lsp)

  name=go-coverage-lsp-$os-$arch
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -q "$out/$name.zip" "$exe")
  else
    tar -C "$stage" -czf "$out/$name.tar.gz" "$exe"
  fi
  rm -rf "$stage"
done

ls "$out"
