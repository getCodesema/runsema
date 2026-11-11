#!/usr/bin/env node
'use strict';

// Starts the binary built for this machine, shipped next to this file, and gets out of the way:
// same arguments, same terminal, same signals, same exit code.
const { spawn } = require('node:child_process');
const { existsSync } = require('node:fs');
const { constants } = require('node:os');
const { join } = require('node:path');

const binary = join(__dirname, `runsema-${process.platform}-${process.arch}`);

if (!existsSync(binary)) {
    console.error(`runsema: no binary for ${process.platform} ${process.arch} (runsema runs on Linux and macOS, x64 or arm64).`);
    process.exit(1);
}

const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit' });

for (const name of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    process.on(name, () => child.kill(name));
}

child.on('error', (error) => {
    console.error(`runsema: ${error.message}`);
    process.exit(127);
});

child.on('exit', (code, signal) => {
    process.exit(signal === null ? code : 128 + constants.signals[signal]);
});
