#!/bin/sh
# Build and embed the console before compiling skalid (including snapshots).
set -eu
cd "$(dirname "$0")/.."
npm ci --prefix web
npm run build --prefix web
node scripts/embed-web.mjs
