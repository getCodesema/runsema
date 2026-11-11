// runsema runs a command with a share of the machine's CPU slots, sized from what is free right now.
//
// Slots are lock files (one per CPU) taken with flock, so the kernel frees them when runsema dies.
// Linux pressure stall information (PSI) delays or shrinks the grant when the machine is already saturated
// by processes that do not go through runsema.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	jobsEnv         = "RUNSEMA_JOBS"
	jobsPlaceholder = "{jobs}"
	pollInterval    = 250 * time.Millisecond
	signalExitBase  = 128
)

type options struct {
	minJobs        int
	maxJobs        int
	pressureMaxPct float64
	timeout        time.Duration
	watchIO        bool
	quiet          bool
	slotDir        string
	cgroupDir      string
	pressureDir    string
}

func main() {
	opts, command := parseArgs(os.Args[1:])
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "usage: runsema [flags] [--] <command> [args...]   ({jobs} in args is replaced by the grant)")
		os.Exit(2)
	}

	jobs, slots := grant(opts)
	code := run(command, jobs)
	release(slots)
	os.Exit(code)
}

func parseArgs(args []string) (options, []string) {
	total := cpuCount()
	opts := options{}
	flags := flag.NewFlagSet("runsema", flag.ExitOnError)
	flags.IntVar(&opts.minJobs, "min", 1, "smallest grant; waits for it until -timeout")
	flags.IntVar(&opts.maxJobs, "max", max(1, total/2), "largest grant (default: half the CPUs, so two full runs fit)")
	flags.Float64Var(&opts.pressureMaxPct, "pressure", 25, "PSI avg10 percentage above which the run waits, then falls back to -min")
	flags.DurationVar(&opts.timeout, "timeout", time.Minute, "longest wait before running anyway with -min")
	flags.BoolVar(&opts.watchIO, "io", false, "also watch IO pressure (CPU and memory are always watched)")
	flags.BoolVar(&opts.quiet, "quiet", false, "do not print the grant on stderr")
	flags.StringVar(&opts.slotDir, "dir", defaultSlotDir(), "directory of the slot lock files, shared by every run")
	_ = flags.Parse(args)
	opts.cgroupDir = "/sys/fs/cgroup"
	opts.pressureDir = "/proc/pressure"
	opts.maxJobs = min(max(opts.maxJobs, 1), total)
	opts.minJobs = min(max(opts.minJobs, 1), opts.maxJobs)
	return opts, flags.Args()
}

// grant returns the number of jobs the command may use and the slots held for it.
// A nested runsema (the variable is already set) reuses its parent's grant and reserves nothing.
func grant(opts options) (int, []*os.File) {
	if inherited, err := strconv.Atoi(os.Getenv(jobsEnv)); err == nil && inherited > 0 {
		return inherited, nil
	}
	jobs, slots, reason := reserve(opts, cpuCount())
	if !opts.quiet {
		fmt.Fprintf(os.Stderr, "runsema: %d jobs (%s)\n", jobs, reason)
	}
	return jobs, slots
}

func reserve(opts options, total int) (int, []*os.File, string) {
	deadline := time.Now().Add(opts.timeout)
	for {
		slots := acquire(opts.slotDir, total, opts.maxJobs)
		pressurePct, resource := worstPressure(opts)
		calm := pressurePct <= opts.pressureMaxPct
		if len(slots) >= opts.minJobs && calm {
			return len(slots), slots, "free slots"
		}
		if !time.Now().Before(deadline) {
			kept := min(len(slots), opts.minJobs)
			release(slots[kept:])
			if calm {
				return opts.minJobs, slots[:kept], "no free slot after timeout"
			}
			return opts.minJobs, slots[:kept], fmt.Sprintf("%s pressure %.0f%% after timeout", resource, pressurePct)
		}
		release(slots)
		time.Sleep(pollInterval)
	}
}

// acquire takes up to want free slots without blocking.
func acquire(dir string, total int, want int) []*os.File {
	_ = os.MkdirAll(dir, 0o700)
	var held []*os.File
	for slot := 0; slot < total && len(held) < want; slot++ {
		file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("slot-%d", slot)), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		if syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			_ = file.Close()
			continue
		}
		held = append(held, file)
	}
	return held
}

func release(slots []*os.File) {
	for _, slot := range slots {
		_ = slot.Close()
	}
}

func worstPressure(opts options) (float64, string) {
	resources := []string{"cpu", "memory"}
	if opts.watchIO {
		resources = append(resources, "io")
	}
	worstPct, worstResource := 0.0, ""
	for _, resource := range resources {
		pct, err := readPressure(pressureFile(opts, resource))
		if err == nil && pct > worstPct {
			worstPct, worstResource = pct, resource
		}
	}
	return worstPct, worstResource
}

// pressureFile is the PSI file of the cgroup the process sees: its own container inside a job, so a busy
// neighbour job does not slow this one down, and the whole machine on a host. Without cgroup v2 PSI it
// is the machine-wide file.
func pressureFile(opts options, resource string) string {
	own := filepath.Join(opts.cgroupDir, resource+".pressure")
	if _, err := os.Stat(own); err == nil {
		return own
	}
	return filepath.Join(opts.pressureDir, resource)
}

// readPressure returns the "some avg10" percentage of a PSI file. A missing file (no PSI) is an error
// the caller treats as no pressure.
func readPressure(path string) (float64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, field := range strings.Fields(strings.SplitN(string(content), "\n", 2)[0]) {
		if value, found := strings.CutPrefix(field, "avg10="); found {
			return strconv.ParseFloat(value, 64)
		}
	}
	return 0, errors.New("no avg10 in " + path)
}

func run(command []string, jobs int) int {
	child := exec.Command(command[0], substitute(command[1:], jobs)...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(), jobsEnv+"="+strconv.Itoa(jobs))

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "runsema:", err)
		return 127
	}
	go func() {
		for received := range signals {
			_ = child.Process.Signal(received)
		}
	}()

	err := child.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return signalExitBase + int(status.Signal())
		}
		return exit.ExitCode()
	}
	return 0
}

func substitute(args []string, jobs int) []string {
	replaced := make([]string, len(args))
	for index, arg := range args {
		replaced[index] = strings.ReplaceAll(arg, jobsPlaceholder, strconv.Itoa(jobs))
	}
	return replaced
}

// cpuCount is the cgroup quota when there is one (containers, CI), the machine otherwise.
func cpuCount() int {
	content, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err == nil {
		fields := strings.Fields(string(content))
		if len(fields) == 2 && fields[0] != "max" {
			quota, quotaErr := strconv.ParseFloat(fields[0], 64)
			period, periodErr := strconv.ParseFloat(fields[1], 64)
			if quotaErr == nil && periodErr == nil && period > 0 {
				return max(1, int((quota+period-1)/period))
			}
		}
	}
	return runtime.NumCPU()
}

func defaultSlotDir() string {
	if dir := os.Getenv("RUNSEMA_DIR"); dir != "" {
		return dir
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "runsema")
	}
	return filepath.Join(os.TempDir(), "runsema-"+strconv.Itoa(os.Getuid()))
}
