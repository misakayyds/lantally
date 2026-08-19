#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-${GITHUB_REF_NAME:-0.1.0-dev}}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
LDFLAGS="-s -w -X github.com/misakayyds/lantally/internal/version.Version=${VERSION} -X github.com/misakayyds/lantally/internal/version.Commit=${COMMIT} -X github.com/misakayyds/lantally/internal/version.Date=${DATE}"
OUT="${OUT:-dist/release}"
rm -rf "$OUT"
mkdir -p "$OUT"

build() {
  local os="$1" arch="$2" extra="$3" bin="$4" pkg="$5"
  local -a buildenv=(CGO_ENABLED=0 "GOOS=${os}" "GOARCH=${arch}")
  if [[ -n "${extra}" ]]; then
    buildenv+=("${extra}")
  fi
  echo "building ${bin}"
  env "${buildenv[@]}" go build -trimpath -ldflags "${LDFLAGS}" -o "${OUT}/${bin}" "${pkg}"
}

build linux amd64 "" lantally-server_linux_amd64 ./cmd/lantally-server
build linux arm64 "" lantally-server_linux_arm64 ./cmd/lantally-server
build linux amd64 "" lantally-agent_linux_amd64 ./cmd/lantally-agent
build linux arm64 "" lantally-agent_linux_arm64 ./cmd/lantally-agent
build linux arm GOARM=7 lantally-agent_linux_armv7 ./cmd/lantally-agent
build linux mips GOMIPS=softfloat lantally-agent_linux_mips_softfloat ./cmd/lantally-agent
build linux mipsle GOMIPS=softfloat lantally-agent_linux_mipsle_softfloat ./cmd/lantally-agent
build darwin amd64 "" lantally-agent_darwin_amd64 ./cmd/lantally-agent
build darwin arm64 "" lantally-agent_darwin_arm64 ./cmd/lantally-agent
build windows amd64 "" lantally-agent_windows_amd64.exe ./cmd/lantally-agent

(
  cd "$OUT"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum * > SHA256SUMS
  else
    shasum -a 256 * > SHA256SUMS
  fi
)

echo "wrote ${OUT}"
