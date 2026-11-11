#!/bin/sh
# Installs the built npm packages in an empty project, as a user would, and runs the command.
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
host="runsema-$(node -p 'process.platform + "-" + process.arch')"

sh "$root/scripts/build-npm.sh" 0.0.0-smoke
(cd "$root/dist/npm/runsema" && npm pack --silent --pack-destination "$work")
(cd "$root/dist/npm/$host" && npm pack --silent --pack-destination "$work")

cd "$work"
npm init --yes > /dev/null
npm install --silent --no-audit --no-fund ./codesema-"$host"-0.0.0-smoke.tgz ./codesema-runsema-0.0.0-smoke.tgz

jobs="$(./node_modules/.bin/runsema -quiet -- sh -c 'echo "$RUNSEMA_JOBS"')"
[ "$jobs" -ge 1 ]
[ "$(./node_modules/.bin/runsema -quiet -- echo '{jobs}')" = "$jobs" ]

code=0
./node_modules/.bin/runsema -quiet -- sh -c 'exit 7' || code=$?
[ "$code" -eq 7 ]

echo "npm smoke ok: $jobs jobs granted through $host"
