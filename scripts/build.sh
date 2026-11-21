#!/usr/bin/env bash
# Build a dynamically linked Bifrost, the Said No plugin and the decoy server
# from source, with one Go toolchain and one set of Bifrost sources (Go plugins
# must match the host binary exactly). Linux or macOS; needs Go 1.27.4 (or
# GOTOOLCHAIN=auto), git and a C compiler.
#
# Usage: scripts/build.sh [build_dir]     # default: ./build
# Outputs: build/bifrost-http, plugin/build/said-no.so, decoy/build/decoy
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "${1:-$here/build}"
out="$(cd "${1:-$here/build}" && pwd)"   # absolute: Go requires an absolute GOWORK path
commit="${BIFROST_COMMIT:-bc27da0d433264d626f59644e8c6937b56358748}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.4}"
if [ ! -d "$out/bifrost/.git" ]; then
  git clone --filter=blob:none https://github.com/maximhq/bifrost.git "$out/bifrost"
fi
git -C "$out/bifrost" fetch -q --depth 1 origin "$commit" 2>/dev/null || true
git -C "$out/bifrost" checkout -q "$commit"
cd "$out/bifrost"
rm -f go.work go.work.sum
go work init ./core ./framework ./transports
for d in $(find plugins cli -name go.mod -not -path '*/node_modules/*' -exec dirname {} \;); do go work use "$d"; done
go work use "$here/plugin"
# Bifrost embeds its web UI. Build it with Node if available; otherwise embed a placeholder.
if [ ! -f transports/bifrost-http/ui/index.html ]; then
  if command -v npm >/dev/null && [ "${SKIP_UI:-0}" != "1" ]; then
    (cd ui && npm ci --no-audit --no-fund && npx vite build && rm -rf ../transports/bifrost-http/ui && cp -r out ../transports/bifrost-http/ui)
  else
    mkdir -p transports/bifrost-http/ui && echo '<html><body>UI not built</body></html>' > transports/bifrost-http/ui/index.html
  fi
fi
(cd transports/bifrost-http && CGO_ENABLED=1 go build -trimpath -o "$out/bifrost-http" .)
(cd "$here/plugin" && GOWORK="$out/bifrost/go.work" CGO_ENABLED=1 go build -trimpath -buildmode=plugin -o build/said-no.so .)
(cd "$here/decoy" && GOWORK=off go build -trimpath -o build/decoy .)
echo "built: $out/bifrost-http, $here/plugin/build/said-no.so, $here/decoy/build/decoy"
