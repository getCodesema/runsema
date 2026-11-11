#!/bin/sh
# Builds the npm packages into dist/npm: the launcher package, and one package per platform holding the
# static binary. npm installs only the platform package that matches the machine.
#   usage: scripts/build-npm.sh <version>
set -eu

version="${1:?usage: scripts/build-npm.sh <version>}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist/npm"

mkdir -p "$out/runsema/bin"
sed "s/0\.0\.0/$version/g" "$root/npm/package.json" > "$out/runsema/package.json"
cp "$root/npm/bin/runsema.js" "$out/runsema/bin/runsema.js"
cp "$root/README.md" "$root/LICENSE" "$out/runsema/"

# <npm os>:<npm cpu>:<go arch>
for target in linux:x64:amd64 linux:arm64:arm64 darwin:x64:amd64 darwin:arm64:arm64; do
    os="${target%%:*}"
    cpu="${target#*:}"
    cpu="${cpu%%:*}"
    package="runsema-$os-$cpu"
    mkdir -p "$out/$package/bin"
    (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="${target##*:}" \
        go build -trimpath -ldflags='-s -w' -o "$out/$package/bin/runsema" .)
    cp "$root/LICENSE" "$out/$package/"
    cat > "$out/$package/package.json" <<JSON
{
    "name": "@codesema/$package",
    "version": "$version",
    "description": "The $os $cpu binary of runsema.",
    "license": "MIT",
    "repository": {
        "type": "git",
        "url": "git+https://github.com/getCodesema/runsema.git"
    },
    "files": [
        "bin"
    ],
    "os": [
        "$os"
    ],
    "cpu": [
        "$cpu"
    ]
}
JSON
done

echo "built $out (version $version)"
