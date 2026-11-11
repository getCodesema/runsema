#!/usr/bin/env node
'use strict';

// Starts the binary of the platform package npm installed next to this one, and gets out of the way:
// same arguments, same terminal, same signals, same exit code.
const { spawn } = require('node:child_process');
const { constants } = require('node:os');

const platformPackage = `runsema-${process.platform}-${process.arch}`;

let binary;
try {
    binary = require.resolve(`${platformPackage}/bin/runsema`);
} catch {
    console.error(`runsema: package ${platformPackage} is not installed (runsema runs on Linux and macOS, x64 or arm64).`);
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
