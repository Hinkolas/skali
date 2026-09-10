#!/bin/sh
# Skali bootstrap: downloads the skali CLI binary for this host from
# GitHub Releases, verifies its sha256 checksum, and installs it. Cluster
# installation then runs through `skali cluster`, which fetches the
# skali-hostd of its own release when a node needs it; a machine that only
# deploys never carries the host daemon.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Hinkolas/skali/main/install.sh | SKALI_CHANNEL=beta sh
#
# Environment:
#   SKALI_CHANNEL   stable (default), or beta to include alpha/beta/rc releases
#   SKALI_VERSION   optional exact release tag; overrides channel selection
#   GITHUB_TOKEN    optional; required while the repository is private
#   SKALI_BASE_URL  override the download base URL (testing only)

set -eu

REPO="Hinkolas/skali"
BINARY="skali"
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
channel="${SKALI_CHANNEL:-stable}"
case "$channel" in stable|beta) ;; *) fail "SKALI_CHANNEL must be stable or beta" ;; esac
version="${SKALI_VERSION:-}"
# Retain the old explicit latest spelling as an unpinned install.
[ "$version" != latest ] || version=""

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Read only release-level fields and asset names/IDs. Tokenizing JSON keeps
# release notes, nested authors, field order, and compact responses out of
# selection. Use POSIX awk so fresh hosts need no jq, Python, or Node runtime.
metadata() {
  awk -v mode="$1" -v wanted="${2:-}" '
    function scalar(value) {
      if (depth == 1) {
        if (key[depth] == "tag_name") tag = value
        if (key[depth] == "draft") draft = value
        if (key[depth] == "prerelease") pre = value
      }
      if (assets && depth == 2) {
        if (key[depth] == "name") name = value
        if (key[depth] == "id") id = value
      }
    }
    { input = input $0 "\n" }
    END {
      for (i = 1; i <= length(input); i++) {
        c = substr(input, i, 1)
        if (c ~ /[ \t\r\n]/) continue
        if (c == "\"") {
          value = ""; closed = 0
          while (++i <= length(input)) {
            c = substr(input, i, 1)
            if (c == "\"") { closed = 1; break }
            if (c == "\\") {
              c = substr(input, ++i, 1)
              # The fields we consume are ASCII. Keep other escapes literal
              # so they cannot turn an invalid tag into a valid one.
              if (c != "\"" && c != "\\" && c != "/") value = value "\\"
            }
            value = value c
          }
          if (!closed) exit 1
          j = i + 1
          while (substr(input, j, 1) ~ /[ \t\r\n]/ && j <= length(input)) j++
          if (substr(input, j, 1) == ":") key[depth] = value
          else scalar(value)
        } else if (c == "{") {
          depth++; key[depth] = ""
          if (depth == 1) { tag = ""; draft = "true"; pre = "true" }
          if (assets && depth == 2) { name = ""; id = "" }
        } else if (c == "}") {
          if (mode == "releases" && depth == 1) printf "%s\t%s\t%s\n", tag, draft, pre
          if (mode == "asset" && assets && depth == 2 && name == wanted && id ~ /^[0-9]+$/) print id
          depth--
          if (depth < 0) exit 1
        } else if (c == "[") {
          if (depth == 1 && key[depth] == "assets") assets = 1
        } else if (c == "]") {
          if (depth == 1) assets = 0
        } else if (c ~ /[0-9tfn-]/) {
          value = c
          while (substr(input, i + 1, 1) ~ /[a-zA-Z0-9.+-]/ && i < length(input)) value = value substr(input, ++i, 1)
          scalar(value)
        }
      }
      if (depth != 0) exit 1
    }
  '
}

api_get() {
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" -H "Accept: application/vnd.github+json" -o "$2" "$1"
  else
    curl -fsSL -H "Accept: application/vnd.github+json" -o "$2" "$1"
  fi
}

if [ -z "$version" ]; then
  if [ -n "${SKALI_BASE_URL:-}" ]; then
    version=local
  else
    if [ "$channel" = stable ]; then
      api_get "${API}/latest" "$tmp/release.json" \
        || fail "cannot find a stable release (use SKALI_CHANNEL=beta for prereleases; check GITHUB_TOKEN for private repositories)"
      metadata releases < "$tmp/release.json" > "$tmp/releases.tsv" || fail "invalid release metadata"
    else
      page=1
      : > "$tmp/releases.tsv"
      while :; do
        api_get "${API}?per_page=100&page=${page}" "$tmp/page.json" \
          || fail "cannot list releases (check network access and GITHUB_TOKEN for private repositories)"
        metadata releases < "$tmp/page.json" > "$tmp/page.tsv" || fail "invalid release metadata"
        cat "$tmp/page.tsv" >> "$tmp/releases.tsv"
        [ "$(wc -l < "$tmp/page.tsv" | tr -d " ")" -eq 100 ] || break
        page=$((page + 1))
      done
    fi
    # The beta channel follows the highest published version, including stable
    # releases. A newly published patch for an older line must not win.
    version=$(awk -F "\t" -v channel="$channel" '
      $1 ~ /^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$/ && $2 == "false" {
        if (channel == "stable" && ($3 != "false" || index($1, "-"))) next
        split(substr($1, 2), parts, /[.-]/)
        rank[1] = parts[1] + 0; rank[2] = parts[2] + 0; rank[3] = parts[3] + 0
        rank[4] = parts[4] == "alpha" ? 0 : parts[4] == "beta" ? 1 : parts[4] == "rc" ? 2 : 3
        rank[5] = parts[5] + 0
        newer = best == ""
        for (i = 1; i <= 5 && best != ""; i++) {
          if (rank[i] != previous[i]) { newer = rank[i] > previous[i]; break }
        }
        if (newer) { best = $1; for (i = 1; i <= 5; i++) previous[i] = rank[i] }
      }
      END { print best }
    ' "$tmp/releases.tsv")
    [ -n "$version" ] || fail "no published release is available on the ${channel} channel"
  fi
elif ! printf "%s\n" "$version" | grep -Eq "^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$"; then
  fail "SKALI_VERSION must be an exact release tag, for example v0.1.0-alpha.1"
fi

# Resolve once above: every binary and its checksums come from the same tag,
# even if a newer release is published while this installation is running.
fetch() {
  if [ -n "${SKALI_BASE_URL:-}" ]; then
    curl -fsSL -o "$2" "${SKALI_BASE_URL}/$1" || fail "download failed: ${SKALI_BASE_URL}/$1"
  elif [ -n "${GITHUB_TOKEN:-}" ]; then
    if [ ! -f "$tmp/release.json" ]; then
      api_get "${API}/tags/${version}" "$tmp/release.json" \
        || fail "cannot read release metadata (check GITHUB_TOKEN and the tag)"
    fi
    id=$(metadata asset "$1" < "$tmp/release.json") || fail "invalid release metadata"
    [ -n "$id" ] || fail "release has no asset named $1"
    # curl drops Authorization on the cross-host storage redirect.
    curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H "Accept: application/octet-stream" -o "$2" "${API}/assets/${id}" \
      || fail "download failed for asset $1"
  else
    url="https://github.com/${REPO}/releases/download/${version}/$1"
    curl -fsSL -o "$2" "$url" \
      || fail "download failed: $url (private repository? set GITHUB_TOKEN)"
  fi
}

log "downloading $asset (${version})"
fetch "$asset" "$tmp/$asset"
fetch "checksums.txt" "$tmp/checksums.txt"

verify() {
  expected=$(awk -v name="$1" '$2 == name { print $1 }' "$tmp/checksums.txt")
  [ -n "$expected" ] || fail "no checksum entry for $1"
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$2" | awk '{ print $1 }')
  else
    actual=$(shasum -a 256 "$2" | awk '{ print $1 }')
  fi
  [ "$actual" = "$expected" ] || fail "checksum mismatch for $1"
}
verify "$asset" "$tmp/$asset"

if [ "$os" = "darwin" ]; then
  # Rootless on macOS: `skali cluster` manages its Lima VM from the user
  # session, so the CLI lives on the user PATH.
  dest="${HOME}/.local/bin"
  mkdir -p "$dest"
  install -m 0755 "$tmp/$asset" "${dest}/${BINARY}"
  log "installed ${dest}/${BINARY}"
  case ":${PATH}:" in
    *":${dest}:"*) ;;
    *) log "note: ${dest} is not on your PATH; add: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
  esac
  log "next: run skali cluster to set up Skali on this Mac"
else
  # Cluster installation must run privileged on Linux, and sudo's
  # secure_path includes /usr/local/bin, so a system path costs nothing
  # extra.
  dest="/usr/local/bin"
  if [ "$(id -u)" = "0" ]; then
    install -m 0755 "$tmp/$asset" "${dest}/${BINARY}"
  else
    log "installing to ${dest} requires sudo"
    sudo install -m 0755 "$tmp/$asset" "${dest}/${BINARY}"
  fi
  log "installed ${dest}/${BINARY}"
  log "next: run sudo skali cluster to set up Skali on this host"
fi
