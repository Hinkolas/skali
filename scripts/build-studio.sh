#!/bin/sh
# Build and embed the Studio before compiling skalid (including snapshots).
set -eu
cd "$(dirname "$0")/.."
npm ci --prefix studio
npm run build --prefix studio
node scripts/embed-studio.mjs
