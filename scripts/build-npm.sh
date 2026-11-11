#!/bin/sh
# Builds the npm package into dist/npm/runsema: the launcher and one static binary per platform, side by
# side. The launcher starts the one that matches the machine.
#   usage: scripts/build-npm.sh <version>
set -eu

version="${1:?usage: scripts/build-npm.sh <version>}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist/npm/runsema"

mkdir -p "$out/bin"
sed "s/0\.0\.0/$version/" "$root/npm/package.json" > "$out/package.json"
cp "$root/npm/bin/runsema.js" "$out/bin/runsema.js"
cp "$root/README.md" "$root/LICENSE" "$out/"

# <node platform>:<node arch>:<go arch>
for target in linux:x64:amd64 linux:arm64:arm64 darwin:x64:amd64 darwin:arm64:arm64; do
    os="${target%%:*}"
    cpu="${target#*:}"
    cpu="${cpu%%:*}"
    (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="${target##*:}" \
        go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$out/bin/runsema-$os-$cpu" .)
done

echo "built $out (version $version)"
