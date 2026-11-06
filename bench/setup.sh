#!/bin/bash
# Build the ocbench image and write the opencode-go credential for run.py.
set -euo pipefail
cd "$(dirname "$0")"
OPENCODE_VERSION=${OPENCODE_VERSION:-2.2.5}
arch=$(uname -m); case $arch in arm64|aarch64) goarch=arm64 oc=linux-arm64 ;; *) goarch=amd64 oc=linux-x64-baseline ;; esac
mkdir -p build && chmod 700 build
cp Dockerfile entry.sh build/
[ -f build/oc.tgz ] || curl -fsSL -o build/oc.tgz "https://opencode.ai/files/bin/$OPENCODE_VERSION/opencode-$oc.tar.gz"
(cd .. && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -o bench/build/explorerepo ./cmd/explorerepo)
# Only the opencode-go credential goes into the containers: $OPENCODE_GO_API_KEY, else the one opencode has stored.
if [ -n "${OPENCODE_GO_API_KEY:-}" ]; then
  python3 -c '
import json, os
json.dump([{"id": "cred_bench", "integrationID": "opencode-go", "label": "OpenCode Go", "active": True,
            "value": {"type": "key", "key": os.environ["OPENCODE_GO_API_KEY"]}}], open("build/go.json", "w"))'
else
  opencode auth export --standalone </dev/null | python3 -c '
import json, sys
json.dump([c for c in json.load(sys.stdin) if c["integrationID"] == "opencode-go"], sys.stdout)' > build/go.json
fi
chmod 600 build/go.json
docker build -q -t ocbench build
