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

# <npm cpu>:<go arch>
for target in x64:amd64 arm64:arm64; do
    cpu="${target%%:*}"
    package="runsema-linux-$cpu"
    mkdir -p "$out/$package/bin"
    (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="${target##*:}" \
        go build -trimpath -ldflags='-s -w' -o "$out/$package/bin/runsema" .)
    cp "$root/LICENSE" "$out/$package/"
    cat > "$out/$package/package.json" <<JSON
{
    "name": "$package",
    "version": "$version",
    "description": "The linux $cpu binary of runsema.",
    "license": "MIT",
    "repository": {
        "type": "git",
        "url": "git+https://github.com/getCodesema/runsema.git"
    },
    "files": [
        "bin"
    ],
    "os": [
        "linux"
    ],
    "cpu": [
        "$cpu"
    ]
}
JSON
done

echo "built $out (version $version)"
