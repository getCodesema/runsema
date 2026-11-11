# runsema

A small launcher that runs a command with a share of the CPUs that are free right now, instead of a
fixed worker count.

```sh
runsema -- vitest run --maxWorkers={jobs}
```

Linux and macOS. Go, standard library only, one static binary.

## Why

Test runners, bundlers and linters size their workers once, from the core count. Two of them started
together each believe they own the machine, and so do the tools of every other terminal, agent session
or worktree. The usual answer is a fixed cap (`--maxWorkers=4`, `--concurrency=2`): too low when the
machine is idle, too high when it is busy.

runsema makes the cap follow the machine. Every run asks for slots before it starts, gets what is free,
and gives it back when it ends. Nothing has to know about anything else: there is no daemon and no
configuration.

## How it works

1. **Slots.** One lock file per CPU, in a directory shared by every run. runsema takes the free ones
   with `flock`, up to `-max`. The kernel frees them when runsema exits, whatever the reason, so a
   killed run never leaks its slots.
2. **Pressure.** It reads Linux pressure stall information (PSI): the share of the last 10 s some task
   spent waiting for CPU or memory. Above `-pressure` it waits, because something that does not go
   through runsema is already loading the machine.
3. **Grant.** When at least `-min` slots are free and the pressure is low, the command starts with that
   many jobs. After `-timeout` it starts anyway with `-min`: runsema delays a run, it never blocks one.
4. **Hand-over.** The grant reaches the command as `{jobs}` replaced in its arguments and as the
   `RUNSEMA_JOBS` environment variable. Use whichever the tool can read.
5. **Nesting.** A runsema started under another one reuses the grant of its parent and reserves
   nothing, so a task runner and the tools it starts can both be wrapped.

The command gets the terminal as is, receives the signals runsema receives, and its exit code is the
exit code of runsema.

Inside a container the CPU count is the cgroup quota (`cpu.max`) and the pressure is the one of the
container itself, so a busy neighbour does not slow the run down. On a host both are machine-wide.

## Install

With npm, pnpm, yarn or bun (the binary ships in the package, nothing is downloaded at install):

```sh
npm install --save-dev @codesema/runsema
```

With Go:

```sh
go install github.com/getCodesema/runsema@latest
```

Or build it and copy the binary anywhere:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o runsema .
```

## Usage

```sh
runsema -- jest --maxWorkers={jobs}
runsema -- turbo run test --concurrency={jobs}
runsema -max 4 -- playwright test --workers={jobs}
runsema -- sh -c 'make -j"$RUNSEMA_JOBS"'
```

| Flag        | Default           | Meaning                                                          |
| ----------- | ----------------- | ---------------------------------------------------------------- |
| `-min`      | `1`               | Smallest grant; the run waits for it until `-timeout`            |
| `-max`      | half the CPUs     | Largest grant, so that two full runs fit side by side            |
| `-pressure` | `25`              | PSI `avg10` percentage above which the run waits                 |
| `-timeout`  | `1m`              | Longest wait before running anyway with `-min`                   |
| `-io`       | off               | Also watch IO pressure (CPU and memory are always watched)       |
| `-quiet`    | off               | Do not print the grant on stderr                                 |
| `-dir`      | see below         | Directory of the slot lock files                                 |

The slot directory is `$RUNSEMA_DIR`, else `$XDG_RUNTIME_DIR/runsema`, else `/tmp/runsema-<uid>`. Runs
share slots only when they share this directory.

## Limits

- The grant is fixed when the command starts: a run does not grow when the machine frees up.
- Nested runs each reuse the whole grant of their parent; it is not divided between siblings.
- A tool that ignores both `{jobs}` and `RUNSEMA_JOBS` is only delayed, not sized.
- Slots count CPUs. Memory is seen through pressure only.
- Without PSI (kernel before 4.20, or PSI disabled) only the slots apply.
- macOS has no PSI: only the slots apply there. No Windows (`flock`).

## Development

```sh
go vet ./... && go test ./...
sh scripts/smoke-npm.sh
```

`main.go` holds everything: `reserve` decides the grant, `acquire` takes the slots, `readPressure`
parses PSI, `run` starts the command. `main_test.go` covers them on temporary directories.

`scripts/build-npm.sh <version>` builds the npm packages into `dist/npm`: the `@codesema/runsema` launcher and one
package per platform holding the binary. `scripts/smoke-npm.sh` installs them in an empty project and
runs the command.

## License

[MIT](LICENSE)
