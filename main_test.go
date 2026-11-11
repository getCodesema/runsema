package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const gb = int64(1) << 30

// newOptions builds a calm machine in temporary directories: no pressure, 32 GB of which 24 are free.
func newOptions(t *testing.T) options {
	t.Helper()
	t.Setenv(jobsEnv, "")
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "pressure"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMeminfo(t, procDir, 32, 24)
	return options{
		minJobs:        1,
		maxJobs:        6,
		pressureMaxPct: 25,
		slotDir:        t.TempDir(),
		stateFile:      filepath.Join(t.TempDir(), "profiles.json"),
		cgroupDir:      t.TempDir(),
		procDir:        procDir,
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeMeminfo(t *testing.T, procDir string, totalGB int64, availableGB int64) {
	t.Helper()
	writeFile(t, filepath.Join(procDir, "meminfo"),
		fmt.Sprintf("MemTotal:       %d kB\nMemFree:        1 kB\nMemAvailable:   %d kB\n", totalGB*1024*1024, availableGB*1024*1024))
}

func writePressure(t *testing.T, path string, avg10 string) {
	t.Helper()
	writeFile(t, path, "some avg10="+avg10+" avg60=1.00 avg300=1.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
}

func TestAcquireSharesSlotsBetweenRuns(t *testing.T) {
	dir := t.TempDir()

	first := acquire(dir, 4, 3)
	second := acquire(dir, 4, 3)
	release(first)
	third := acquire(dir, 4, 3)

	if len(first) != 3 || len(second) != 1 || len(third) != 3 {
		t.Fatalf("got %d, %d, %d slots; want 3, 1, 3", len(first), len(second), len(third))
	}
}

func TestAdmitGivesAnUnknownCommandTheFreeSlotsAndTheGate(t *testing.T) {
	opts := newOptions(t)

	admitted := admit(opts, "key", 8)

	if admitted.jobs != 6 || len(admitted.slots) != 6 || admitted.gate == nil {
		t.Fatalf("got %d jobs, %d slots, gate %v; want 6, 6, held", admitted.jobs, len(admitted.slots), admitted.gate)
	}
	if admitted.reason != "first run of this command, measuring; 24.0 GB available" {
		t.Fatalf("got reason %q", admitted.reason)
	}
}

func TestAdmitSizesAKnownCommandOnTheMemoryLeft(t *testing.T) {
	opts := newOptions(t)
	writeMeminfo(t, opts.procDir, 32, 10)
	if err := recordRun(opts.stateFile, "key", profileRun{Jobs: 4, PeakBytes: 8 * gb}, false); err != nil {
		t.Fatal(err)
	}

	admitted := admit(opts, "key", 8)

	// 10 GB free, 3.2 kept in reserve, 2 GB per job with a 1.18 margin: 2 jobs.
	if admitted.jobs != 2 || len(admitted.slots) != 2 {
		t.Fatalf("got %d jobs, %d slots; want 2, 2", admitted.jobs, len(admitted.slots))
	}
	if admitted.reason != "2.00 GB per job learned, 10.0 GB available" {
		t.Fatalf("got reason %q", admitted.reason)
	}
}

func TestAdmitCountsTheMemoryARunningCommandHasNotLoadedYet(t *testing.T) {
	opts := newOptions(t)
	if err := recordRun(opts.stateFile, "heavy", profileRun{Jobs: 4, PeakBytes: 8 * gb}, false); err != nil {
		t.Fatal(err)
	}
	first := admit(opts, "heavy", 12)
	_ = first.gate.Close()

	second := admit(opts, "heavy", 12)

	// 24 GB free: the first takes 6 jobs and announces 12 GB, so the second sizes itself on 12 GB.
	if first.jobs != 6 || second.jobs != 3 {
		t.Fatalf("got %d then %d jobs; want 6 then 3", first.jobs, second.jobs)
	}
	dropReservation(first.reservation)
	release(append(first.slots, second.gate))

	third := admit(opts, "heavy", 12)

	// The second still announces 6 GB: 18 GB left, enough for the 6 jobs a run may have.
	if third.jobs != 6 {
		t.Fatalf("got %d jobs once the first run ended; want 6", third.jobs)
	}
}

func TestAdmitQueuesBehindARunThatIsLoading(t *testing.T) {
	opts := newOptions(t)
	loading := admit(opts, "first", 8)
	defer release(append(loading.slots, loading.gate))

	queued := admit(opts, "second", 8)

	if queued.jobs != 1 || queued.gate != nil || len(queued.slots) != 0 || queued.reason != "another run is loading, timeout reached" {
		t.Fatalf("got %d jobs, gate %v, %d slots, %q", queued.jobs, queued.gate, len(queued.slots), queued.reason)
	}
}

func TestAdmitPassesOnceTheLoadingRunOpensTheGate(t *testing.T) {
	opts := newOptions(t)
	opts.timeout = 5 * time.Second
	loading := admit(opts, "first", 8)
	defer release(loading.slots)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = loading.gate.Close()
	}()

	next := admit(opts, "second", 8)

	if next.jobs != 2 || next.gate == nil {
		t.Fatalf("got %d jobs, gate %v; want the 2 slots left and the gate", next.jobs, next.gate)
	}
}

func TestAdmitWaitsWhenMemoryIsShort(t *testing.T) {
	opts := newOptions(t)
	writeMeminfo(t, opts.procDir, 32, 3)

	admitted := admit(opts, "key", 8)

	if admitted.jobs != 1 || admitted.gate != nil || admitted.reason != "not enough memory (3.0 GB available), timeout reached" {
		t.Fatalf("got %d jobs, gate %v, %q", admitted.jobs, admitted.gate, admitted.reason)
	}
}

func TestAdmitWaitsUnderPressure(t *testing.T) {
	opts := newOptions(t)
	writePressure(t, filepath.Join(opts.procDir, "pressure", "memory"), "80.00")

	admitted := admit(opts, "key", 8)

	if admitted.jobs != 1 || admitted.reason != "memory pressure 80%, timeout reached" {
		t.Fatalf("got %d jobs, %q", admitted.jobs, admitted.reason)
	}
}

func TestAdmitReadsItsOwnCgroupPressureBeforeTheMachine(t *testing.T) {
	opts := newOptions(t)
	writePressure(t, filepath.Join(opts.cgroupDir, "cpu.pressure"), "2.00")
	writePressure(t, filepath.Join(opts.procDir, "pressure", "cpu"), "95.00")

	admitted := admit(opts, "key", 8)

	if admitted.jobs != 6 {
		t.Fatalf("got %d jobs; want 6", admitted.jobs)
	}
}

func TestAdmitIgnoresIOPressureUnlessAsked(t *testing.T) {
	opts := newOptions(t)
	writePressure(t, filepath.Join(opts.procDir, "pressure", "io"), "90.00")

	ignored := admit(opts, "key", 8)
	release(append(ignored.slots, ignored.gate))
	opts.watchIO = true
	watched := admit(opts, "key", 8)

	if ignored.jobs != 6 || watched.jobs != 1 {
		t.Fatalf("got %d without -io, %d with; want 6, 1", ignored.jobs, watched.jobs)
	}
}

func TestAdmitNestedReusesTheGrantOfItsParent(t *testing.T) {
	opts := newOptions(t)
	t.Setenv(jobsEnv, "5")

	admitted := admit(opts, "key", 8)

	if admitted.jobs != 5 || !admitted.nested || admitted.gate != nil || len(admitted.slots) != 0 {
		t.Fatalf("got %+v", admitted)
	}
}

func TestSubstitute(t *testing.T) {
	got := substitute([]string{"run", "--maxWorkers={jobs}", "-j", "{jobs}"}, 8)

	want := []string{"run", "--maxWorkers=8", "-j", "8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v; want %v", got, want)
	}
}
