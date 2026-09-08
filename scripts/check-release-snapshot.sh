#!/bin/sh
# Nonpublishing rehearsal; requires GoReleaser and the configured Go toolchain.
set -eu
goreleaser release --snapshot --clean --skip=docker,publish
snapshot_version=$(python3 -c 'import json; print(json.load(open("dist/metadata.json"))["version"])')
go run ./cmd/skali-schema --schema release --version "v$snapshot_version" --output ".cache/skali-release/$snapshot_version/release.json" --verify
case "$(uname -s)/$(uname -m)" in
 Darwin/arm64) snapshot_binary=dist/skali_darwin_arm64_v8.0/skali ;;
 Darwin/x86_64) snapshot_binary=dist/skali_darwin_amd64_v1/skali ;;
 Linux/x86_64) snapshot_binary=dist/skali_linux_amd64_v1/skali ;;
 Linux/aarch64) snapshot_binary=dist/skali_linux_arm64_v8.0/skali ;;
 *) echo 'unsupported rehearsal host' >&2; exit 1 ;;
esac
"$snapshot_binary" --version | python3 -c 'import sys; expected="v"+sys.argv[1]; actual=sys.stdin.read(); assert expected in actual, (expected,actual)' "$snapshot_version"
