#!/usr/bin/env bash
# Builds forge with embedded build metadata so the in-app updater can compare
# the running binary against the source repo and pull updates.
#
# Usage:
#   ./scripts/build.sh                # builds ./forge in repo root
#   ./scripts/build.sh bin/forge      # custom output path

set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-forge}"

cd "$repo"

sha="$(git rev-parse --short HEAD)"
branch="$(git rev-parse --abbrev-ref HEAD)"
dirty=""
if [ -n "$(git status --porcelain)" ]; then
    dirty="-dirty"
fi
version="${branch}${dirty}"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

ldflags="-X 'main.Version=${version}' -X 'main.BuildSHA=${sha}' -X 'main.BuildTime=${build_time}' -X 'main.SourceRepo=${repo}'"

echo "Building forge → ${out}"
echo "  version:    ${version}"
echo "  commit:     ${sha}"
echo "  built:      ${build_time}"
echo "  source:     ${repo}"

go build -ldflags "${ldflags}" -o "${out}" ./cmd/forge

echo "Done. Run ./${out} version to verify the embedded metadata."
