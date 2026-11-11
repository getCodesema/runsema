#!/bin/sh
# Installs the built npm package in an empty project, as a user would, and runs the command.
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"

sh "$root/scripts/build-npm.sh" 0.0.0-smoke
(cd "$root/dist/npm/runsema" && npm pack --silent --pack-destination "$work")

cd "$work"
npm init --yes > /dev/null
npm install --silent --no-audit --no-fund ./codesema-runsema-0.0.0-smoke.tgz

jobs="$(./node_modules/.bin/runsema -quiet -- sh -c 'echo "$RUNSEMA_JOBS"')"
[ "$jobs" -ge 1 ]
[ "$(./node_modules/.bin/runsema -quiet -- echo '{jobs}')" = "$jobs" ]

code=0
./node_modules/.bin/runsema -quiet -- sh -c 'exit 7' || code=$?
[ "$code" -eq 7 ]

[ "$(./node_modules/.bin/runsema -version)" = "0.0.0-smoke" ]

echo "npm smoke ok: $jobs jobs granted"
