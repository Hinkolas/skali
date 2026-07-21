#!/bin/sh
# Skali bootstrap: downloads the skali-installer binary for this host from
# GitHub Releases, verifies its sha256 checksum, and installs it.
#
# Usage:
#   curl -fsSL https://github.com/Hinkolas/skali/releases/latest/download/install.sh | sh
#
# Environment:
#   SKALI_VERSION   pin a release tag, for example v0.1.0 (default: latest)
#   GITHUB_TOKEN    optional; required while the repository is private
#   SKALI_BASE_URL  override the download base URL (testing only)

set -eu

REPO="Hinkolas/skali"
BINARY="skali-installer"
API="https://api.github.com/repos/${REPO}/releases"

log()  { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"

os=$(uname -s)
case "$os" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported operating system: $os" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac
asset="${BINARY}_${os}_${arch}"
version="${SKALI_VERSION:-latest}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# fetch NAME DEST resolves one release asset, in priority order:
#   SKALI_BASE_URL  flat directory of assets (local testing)
#   GITHUB_TOKEN    GitHub API asset download; the direct release URLs do
#                   not accept token auth on private repositories, so the
#                   asset id is resolved from the release JSON first
#   default         public release URLs
fetch() {
  if [ -n "${SKALI_BASE_URL:-}" ]; then
    curl -fsSL -o "$2" "${SKALI_BASE_URL}/$1" || fail "download failed: ${SKALI_BASE_URL}/$1"
  elif [ -n "${GITHUB_TOKEN:-}" ]; then
    if [ "$version" = "latest" ]; then rel="${API}/latest"; else rel="${API}/tags/${version}"; fi
    if [ ! -f "$tmp/release.json" ]; then
      curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" -o "$tmp/release.json" "$rel" \
        || fail "cannot read release metadata (check GITHUB_TOKEN and the tag)"
    fi
    id=$(awk -v name="$1" '
      /"url":/  { if (match($0, /releases\/assets\/[0-9]+/)) last = substr($0, RSTART + 16, RLENGTH - 16) }
      /"name":/ { if (index($0, "\"" name "\"")) { print last; exit } }
    ' "$tmp/release.json")
    [ -n "$id" ] || fail "release has no asset named $1"
    # curl drops the Authorization header on the cross-host redirect to the
    # storage backend, which is exactly what is needed here.
    curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H "Accept: application/octet-stream" -o "$2" "${API}/assets/${id}" \
      || fail "download failed for asset $1"
  else
    if [ "$version" = "latest" ]; then
      url="https://github.com/${REPO}/releases/latest/download/$1"
    else
      url="https://github.com/${REPO}/releases/download/${version}/$1"
    fi
    curl -fsSL -o "$2" "$url" \
      || fail "download failed: $url (private repository? set GITHUB_TOKEN)"
  fi
}

log "downloading $asset (${version})"
fetch "$asset" "$tmp/$asset"
fetch "checksums.txt" "$tmp/checksums.txt"

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "no checksum entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset"

if [ "$os" = "darwin" ]; then
  # Rootless on macOS: the darwin-mode installer manages its Lima VM from
  # the user session, so it lives on the user PATH.
  dest="${HOME}/.local/bin"
  mkdir -p "$dest"
  install -m 0755 "$tmp/$asset" "${dest}/${BINARY}"
  log "installed ${dest}/${BINARY}"
  case ":${PATH}:" in
    *":${dest}:"*) ;;
    *) log "note: ${dest} is not on your PATH; add: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
  esac
  log "next: run ${BINARY} to set up Skali on this Mac"
else
  # The installer itself must run privileged on Linux, so a system path
  # (and sudo here) costs nothing extra.
  dest="/usr/local/bin"
  if [ "$(id -u)" = "0" ]; then
    install -m 0755 "$tmp/$asset" "${dest}/${BINARY}"
  else
    log "installing to ${dest} requires sudo"
    sudo install -m 0755 "$tmp/$asset" "${dest}/${BINARY}"
  fi
  log "installed ${dest}/${BINARY}"
  log "next: run sudo ${BINARY} to set up Skali on this host"
fi
