#!/bin/bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
compiler="${SWARMCOPY_GO:-go}"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$compiler" build -buildvcs=false -trimpath -ldflags="-s -w" -o dist/swarmcopy-linux-x86_64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$compiler" build -buildvcs=false -trimpath -ldflags="-s -w" -o dist/swarmcopy-linux-aarch64 .
(cd dist && sha256sum swarmcopy-linux-x86_64 swarmcopy-linux-aarch64 > SHA256SUMS)

