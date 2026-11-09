#!/usr/bin/env bash
# Refresh the checked-in TypeScript SDK snapshot at an explicit commit.
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
revision="${1:-$(cat "$script_dir/typescript/REVISION")}"
if [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Usage: $0 <full 40-character typescript-sdk commit SHA>" >&2
  exit 1
fi
stage="$(mktemp -d "$script_dir/.typescript.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
base="https://raw.githubusercontent.com/agentclientprotocol/typescript-sdk/$revision"
for version in v1 v2; do
  # The unstable views carry every method and type; the façade tables mark what is unstable.
  upstream="src/$version/unstable/schema"
  mkdir -p "$stage/$version"
  for name in types.gen.ts index.ts zod.gen.ts guards.gen.ts; do
    curl --fail --silent --show-error --location "$base/$upstream/$name" -o "$stage/$version/$name"
  done
done
curl --fail --silent --show-error --location "$base/src/core/schema-deserialize.ts" -o "$stage/schema-deserialize.ts"
curl --fail --silent --show-error --location "$base/LICENSE" -o "$stage/LICENSE"
printf '%s\n' "$revision" > "$stage/REVISION"
# Do not replace any source until all downloads have succeeded.
cp -R "$stage/." "$script_dir/typescript/"
printf 'Updated TypeScript SDK snapshot to %s. Run go generate ./... and both module test suites.\n' "$revision"
