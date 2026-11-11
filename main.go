// runsema runs a command with a share of the machine sized from what is free right now.
//
// CPU: slots are lock files (one per CPU) taken with flock, so the kernel frees them when runsema dies.
// Memory: runs enter one at a time through a gate, each keeps it closed while it loads, and runsema
// remembers what every command used to decide how many jobs the next run of it may have.
// Linux pressure stall information (PSI) delays a run when the machine is already saturated.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	jobsEnv         = "RUNSEMA_JOBS"
	jobsPlaceholder = "{jobs}"
	pollInterval    = 250 * time.Millisecond
	sampleInterval  = time.Second
	// Longest time a starting run keeps the gate closed.
	rampMax        = 30 * time.Second
	signalExitBase = 128
	bytesPerGB     = 1 << 30
)

type options struct {
	minJobs        int
	maxJobs        int
	pressureMaxPct float64
	timeout        time.Duration
	watchIO        bool
	quiet          bool
	slotDir        string
	stateFile      string
	cgroupDir      string
	procDir        string
}

// admission is what a run was granted. A run admitted after the timeout holds neither slots nor gate.
type admission struct {
	jobs        int
	slots       []*os.File
	gate        *os.File
	reservation *os.File
	key         string
	nested      bool
	reason      string
}

type usage struct {
	peakBytes int64
	starved   bool
}

func main() {
	opts, command := parseArgs(os.Args[1:])
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "usage: runsema [flags] [--] <command> [args...]   ({jobs} in args is replaced by the grant)")
		os.Exit(2)
	}

	directory, _ := os.Getwd()
	admitted := admit(opts, commandKey(directory, command), cpuCount())
	if !opts.quiet && !admitted.nested {
		fmt.Fprintf(os.Stderr, "runsema: %d jobs (%s)\n", admitted.jobs, admitted.reason)
	}
	code, used := run(opts, command, admitted)
	if !admitted.nested && used.peakBytes > 0 {
		_ = recordRun(opts.stateFile, admitted.key, profileRun{Jobs: admitted.jobs, PeakBytes: used.peakBytes}, used.starved)
	}
	release(admitted.slots)
	dropReservation(admitted.reservation)
	os.Exit(code)
}

func parseArgs(args []string) (options, []string) {
	total := cpuCount()
	opts := options{}
	flags := flag.NewFlagSet("runsema", flag.ExitOnError)
	flags.IntVar(&opts.minJobs, "min", 1, "smallest grant; waits for it until -timeout")
	flags.IntVar(&opts.maxJobs, "max", max(1, total/2), "largest grant (default: half the CPUs, so two full runs fit)")
	flags.Float64Var(&opts.pressureMaxPct, "pressure", 25, "PSI avg10 percentage above which the run waits")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "longest wait before running anyway with -min")
	flags.BoolVar(&opts.watchIO, "io", false, "also watch IO pressure (CPU and memory are always watched)")
	flags.BoolVar(&opts.quiet, "quiet", false, "do not print the grant on stderr")
	flags.StringVar(&opts.slotDir, "dir", defaultSlotDir(), "directory of the slot and gate lock files, shared by every run")
	flags.StringVar(&opts.stateFile, "state", "", "file remembering what each command used (default: in the user cache directory)")
	_ = flags.Parse(args)
	if opts.stateFile == "" {
		opts.stateFile = defaultStateFile(opts.slotDir)
	}
	opts.cgroupDir = "/sys/fs/cgroup"
	opts.procDir = "/proc"
	opts.maxJobs = min(max(opts.maxJobs, 1), total)
	opts.minJobs = min(max(opts.minJobs, 1), opts.maxJobs)
	return opts, flags.Args()
}

// admit waits for the gate, takes the free slots and keeps as many as memory allows.
// A nested runsema (the variable is already set) reuses its parent's grant and reserves nothing.
func admit(opts options, key string, total int) admission {
	if inherited, err := strconv.Atoi(os.Getenv(jobsEnv)); err == nil && inherited > 0 {
		return admission{jobs: inherited, nested: true}
	}
	saved := loadStore(opts.stateFile)
	perJobBytes := bytesPerJob(saved.Commands[key])
	deadline := time.Now().Add(opts.timeout)
	for {
		waiting := "another run is loading"
		if gate := tryLock(filepath.Join(opts.slotDir, "gate")); gate != nil {
			slots := acquire(opts.slotDir, total, opts.maxJobs)
			mem := readMemory(opts)
			mem.availableBytes = max(0, mem.availableBytes-outstandingBytes(opts))
			pressurePct, resource := worstPressure(opts)
			fit := jobsThatFit(len(slots), mem, perJobBytes, saved.Margin)
			if fit >= opts.minJobs && pressurePct <= opts.pressureMaxPct {
				release(slots[fit:])
				return admission{
					jobs:        fit,
					slots:       slots[:fit],
					gate:        gate,
					reservation: reserveMemory(opts, int64(fit)*perJobBytes),
					key:         key,
					reason:      grantReason(mem, perJobBytes),
				}
			}
			release(slots)
			_ = gate.Close()
			waiting = waitReason(len(slots), fit, mem, pressurePct, resource, opts)
		}
		if !time.Now().Before(deadline) {
			return admission{jobs: opts.minJobs, key: key, reason: waiting + ", timeout reached"}
		}
		time.Sleep(pollInterval)
	}
}

func grantReason(mem memory, perJobBytes int64) string {
	if mem.totalBytes <= 0 {
		return "free slots"
	}
	availableGB := float64(mem.availableBytes) / bytesPerGB
	if perJobBytes <= 0 {
		return fmt.Sprintf("first run of this command, measuring; %.1f GB available", availableGB)
	}
	return fmt.Sprintf("%.2f GB per job learned, %.1f GB available", float64(perJobBytes)/bytesPerGB, availableGB)
}

func waitReason(slots int, fit int, mem memory, pressurePct float64, resource string, opts options) string {
	switch {
	case pressurePct > opts.pressureMaxPct:
		return fmt.Sprintf("%s pressure %.0f%%", resource, pressurePct)
	case slots < opts.minJobs:
		return "no free slot"
	case fit < opts.minJobs:
		return fmt.Sprintf("not enough memory (%.1f GB available)", float64(mem.availableBytes)/bytesPerGB)
	}
	return "waiting"
}

// tryLock takes an exclusive lock without waiting, nil when someone holds it.
func tryLock(path string) *os.File {
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	if syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		_ = file.Close()
		return nil
	}
	return file
}

// acquire takes up to want free slots without blocking.
func acquire(dir string, total int, want int) []*os.File {
	var held []*os.File
	for slot := 0; slot < total && len(held) < want; slot++ {
		if file := tryLock(filepath.Join(dir, fmt.Sprintf("slot-%d", slot))); file != nil {
			held = append(held, file)
		}
	}
	return held
}

func release(slots []*os.File) {
	for _, slot := range slots {
		_ = slot.Close()
	}
}

func run(opts options, command []string, admitted admission) (int, usage) {
	child := exec.Command(command[0], substitute(command[1:], admitted.jobs)...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(), jobsEnv+"="+strconv.Itoa(admitted.jobs))

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "runsema:", err)
		if admitted.gate != nil {
			_ = admitted.gate.Close()
		}
		return 127, usage{}
	}
	go func() {
		for received := range signals {
			_ = child.Process.Signal(received)
		}
	}()

	// A command already measured announced what it will use: the next run can enter at once. One never
	// measured keeps the gate closed while it loads.
	gate := admitted.gate
	if admitted.reservation != nil {
		announce(admitted.reservation, child.Process.Pid)
		if gate != nil {
			_ = gate.Close()
			gate = nil
		}
	}

	done := make(chan struct{})
	observed := make(chan usage, 1)
	go func() { observed <- observe(opts, child.Process.Pid, gate, done) }()
	err := child.Wait()
	close(done)
	used := <-observed

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return signalExitBase + int(status.Signal()), used
		}
		return exit.ExitCode(), used
	}
	return 0, used
}

// observe measures the command while it runs. It opens the gate for the next run once the memory of
// this one has stopped growing, and reports its peak and whether the machine ran short of memory.
func observe(opts options, pid int, gate *os.File, done <-chan struct{}) usage {
	var used usage
	var samplesBytes []int64
	started := time.Now()
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			if gate != nil {
				_ = gate.Close()
			}
			return used
		case <-ticker.C:
			treeBytes := treeAnonBytes(opts.procDir, pid)
			used.peakBytes = max(used.peakBytes, treeBytes)
			samplesBytes = append(samplesBytes, treeBytes)
			if gate != nil && (rampStable(samplesBytes) || time.Since(started) >= rampMax) {
				_ = gate.Close()
				gate = nil
			}
			memoryPressurePct, _ := readPressure(pressureFile(opts, "memory"))
			if starving(readMemory(opts)) || memoryPressurePct > opts.pressureMaxPct {
				used.starved = true
			}
		}
	}
}

func substitute(args []string, jobs int) []string {
	replaced := make([]string, len(args))
	for index, arg := range args {
		replaced[index] = strings.ReplaceAll(arg, jobsPlaceholder, strconv.Itoa(jobs))
	}
	return replaced
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

// defaultStateFile lives in the user cache, which survives a reboot; the slot directory often does not.
func defaultStateFile(slotDir string) string {
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "runsema", "profiles.json")
	}
	return filepath.Join(slotDir, "profiles.json")
}

// A reservation is the memory a measured command is expected to use, announced while it loads so that
// the runs admitted behind it do not count the same free memory twice. The file is locked by its run:
// the kernel drops the lock when the run dies, which marks the reservation as stale.
const reservationFormat = "%d %d\n"

func reservationDir(opts options) string {
	return filepath.Join(opts.slotDir, "reservations")
}

// reserveMemory is called with the gate held. A command never measured reserves nothing.
func reserveMemory(opts options, expectedBytes int64) *os.File {
	if expectedBytes <= 0 {
		return nil
	}
	file := tryLock(filepath.Join(reservationDir(opts), strconv.Itoa(os.Getpid())))
	if file != nil {
		_, _ = fmt.Fprintf(file, reservationFormat, 0, expectedBytes)
	}
	return file
}

// announce tells which process tree the reservation is for, so its loaded memory can be deducted.
func announce(reservation *os.File, pid int) {
	var unknownPid int
	var expectedBytes int64
	content, err := os.ReadFile(reservation.Name())
	if err != nil {
		return
	}
	if _, err := fmt.Sscanf(string(content), reservationFormat, &unknownPid, &expectedBytes); err != nil {
		return
	}
	if err := reservation.Truncate(0); err == nil {
		_, _ = reservation.WriteAt([]byte(fmt.Sprintf(reservationFormat, pid, expectedBytes)), 0)
	}
}

func dropReservation(reservation *os.File) {
	if reservation != nil {
		_ = os.Remove(reservation.Name())
		_ = reservation.Close()
	}
}

// outstandingBytes is the memory the running commands announced and have not loaded yet. Called with
// the gate held, so no reservation is being created while the stale ones are removed.
func outstandingBytes(opts options) int64 {
	entries, err := os.ReadDir(reservationDir(opts))
	if err != nil {
		return 0
	}
	var totalBytes int64
	for _, entry := range entries {
		path := filepath.Join(reservationDir(opts), entry.Name())
		if stale := tryLock(path); stale != nil {
			_ = os.Remove(path)
			_ = stale.Close()
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var pid int
		var expectedBytes int64
		if _, err := fmt.Sscanf(string(content), reservationFormat, &pid, &expectedBytes); err != nil {
			continue
		}
		var loadedBytes int64
		if pid > 0 {
			loadedBytes = treeAnonBytes(opts.procDir, pid)
		}
		totalBytes += max(0, expectedBytes-loadedBytes)
	}
	return totalBytes
}
