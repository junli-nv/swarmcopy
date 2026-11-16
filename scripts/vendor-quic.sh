#!/bin/bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
compiler="${SWARMCOPY_GO:-go}"
"$compiler" mod vendor
git apply --check patches/quic-go-pmtu.patch
git apply patches/quic-go-pmtu.patch
