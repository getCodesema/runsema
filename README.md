# runsema

A small launcher that runs a command with the share of the machine that is free right now, CPU and
memory, instead of a fixed worker count.

```sh
runsema -- vitest run --maxWorkers={jobs}
```

Linux and macOS. Go, standard library only, one static binary.

## Why

Test runners, bundlers and linters size their workers once, from the core count. Two of them started
together each believe they own the machine, and so do the tools of every other terminal, agent session
or worktree. The usual answer is a fixed cap (`--maxWorkers=4`, `--concurrency=2`): too low when the
machine is idle, too high when it is busy. And a cap on workers says nothing about memory, which is
what kills processes.

runsema makes the cap follow the machine. Every run asks before it starts, gets what is free, and gives
it back when it ends. It learns what each command uses, so nothing has to be tuned. There is no daemon
and no configuration.

## How it works

1. **Slots.** One lock file per CPU, in a directory shared by every run. runsema takes the free ones
   with `flock`, up to `-max`. The kernel frees them when runsema exits, whatever the reason, so a
   killed run never leaks its slots.
2. **Learning.** While a command runs, runsema measures the memory of its whole process tree. When it
   ends, the peak is stored with the number of jobs it had. The weight of a command is the heaviest of
   its last 10 runs, per job: a suite that grows counts from its next run.
3. **Memory.** A command already measured gets as many jobs as fit in the memory available right now,
   keeping 10% of the machine free. It announces what it is about to use, so the runs admitted behind
   it do not count the same free memory twice while it loads.
4. **Gate.** Runs are admitted one at a time. A command never measured keeps the gate closed until its
   memory stops growing (30 s at most): it loads alone, then the next run decides on real figures.
5. **Margin.** Every weight is multiplied by a safety margin, 1.2 at first and never under 1.1. A run
   during which the machine ran short of memory widens it by a quarter; a quiet run tightens it a
   little. The margin tunes itself.
6. **Pressure.** runsema reads Linux pressure stall information (PSI): the share of the last 10 s some
   task spent waiting for CPU or memory. Above `-pressure` it waits, because something that does not
   go through runsema is already loading the machine.
7. **Grant.** The command starts with the jobs that passed all of the above. After `-timeout` it starts
   anyway with `-min`: runsema delays a run, it never blocks one.
8. **Hand-over.** The grant reaches the command as `{jobs}` replaced in its arguments and as the
   `RUNSEMA_JOBS` environment variable. Use whichever the tool can read.
9. **Nesting.** A runsema started under another one reuses the grant of its parent and reserves
   nothing, so a task runner and the tools it starts can both be wrapped.

The command gets the terminal as is, receives the signals runsema receives, and its exit code is the
exit code of runsema.

Inside a container the CPU count is the cgroup quota (`cpu.max`) and the pressure is the one of the
container itself, so a busy neighbour does not slow the run down. On a host both are machine-wide.
Memory follows the tightest limit (`memory.max`) set on the cgroup of the run or on one above it: a
container, a systemd scope or slice. Without any, it is the memory of the machine.

## Install

With npm, pnpm, yarn or bun. One package holds the binary of every platform and starts the one that
matches the machine, so nothing is downloaded at install and there is no optional dependency to resolve:

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
| `-timeout`  | `5m`              | Longest wait before running anyway with `-min`                   |
| `-io`       | off               | Also watch IO pressure (CPU and memory are always watched)       |
| `-quiet`    | off               | Do not print the grant on stderr                                 |
| `-version`  |                   | Print the version and exit                                       |
| `-dir`      | see below         | Directory of the lock files (slots, gate, reservations)          |
| `-state`    | see below         | File remembering what each command used                          |

The lock directory is `$RUNSEMA_DIR`, else `$XDG_RUNTIME_DIR/runsema`, else `/tmp/runsema-<uid>`. Runs
share the machine only when they share this directory. What runsema learned is kept in
`runsema/profiles.json` under the user cache directory (`~/.cache` on Linux); delete it to forget.

A command is recognised by its directory and its arguments: the same script in another worktree is
measured again.

## Limits

- The grant is fixed when the command starts: a run does not grow when the machine frees up, and a
  run that outgrows what it announced is not stopped.
- Memory is counted per job, as if it grew in proportion: a command with a large fixed part is
  overestimated at low job counts.
- A command that ends in under a second is never measured.
- Nested runs each reuse the whole grant of their parent; it is not divided between siblings.
- A tool that ignores both `{jobs}` and `RUNSEMA_JOBS` is only delayed, not sized.
- Without PSI (kernel before 4.20, or PSI disabled) the slots and the memory still apply.
- macOS has neither PSI nor `/proc`: only the slots apply there. No Windows (`flock`).

## Development

```sh
go vet ./... && go test ./...
sh scripts/smoke-npm.sh
```

`admission.go` holds the decisions as pure functions (`bytesPerJob`, `jobsThatFit`, `rampStable`,
`nextMargin`). `memory.go` reads the machine: memory, process trees, PSI. `profile.go` stores what was
learned. `main.go` is the flow: gate, slots, reservations, the command, the measure. Each has its test
file, on temporary directories.

`scripts/build-npm.sh <version>` builds the npm package into `dist/npm/runsema`: the launcher and one
binary per platform. `scripts/smoke-npm.sh` installs it in an empty project and runs the command.

## License

[MIT](LICENSE)
