package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writePressure(t *testing.T, dir string, resource string, avg10 string) {
	t.Helper()
	content := "some avg10=" + avg10 + " avg60=1.00 avg300=1.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
	if err := os.WriteFile(filepath.Join(dir, resource), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadPressure(t *testing.T) {
	dir := t.TempDir()
	writePressure(t, dir, "cpu", "42.50")

	pct, err := readPressure(filepath.Join(dir, "cpu"))

	if err != nil || pct != 42.5 {
		t.Fatalf("got %v, %v; want 42.5, nil", pct, err)
	}
}

func TestReadPressureMissingFile(t *testing.T) {
	if _, err := readPressure(filepath.Join(t.TempDir(), "cpu")); err == nil {
		t.Fatal("want an error when PSI is unavailable")
	}
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

func TestReserveTakesFreeSlotsWhenCalm(t *testing.T) {
	pressureDir := t.TempDir()
	writePressure(t, pressureDir, "cpu", "1.00")
	opts := options{minJobs: 1, maxJobs: 6, pressureMaxPct: 25, timeout: time.Second, slotDir: t.TempDir(), pressureDir: pressureDir}

	jobs, slots, _ := reserve(opts, 8)

	if jobs != 6 || len(slots) != 6 {
		t.Fatalf("got %d jobs, %d slots; want 6, 6", jobs, len(slots))
	}
}

func TestReserveFallsBackToMinUnderPressure(t *testing.T) {
	pressureDir := t.TempDir()
	writePressure(t, pressureDir, "cpu", "1.00")
	writePressure(t, pressureDir, "memory", "80.00")
	opts := options{minJobs: 2, maxJobs: 6, pressureMaxPct: 25, timeout: 0, slotDir: t.TempDir(), pressureDir: pressureDir}

	jobs, slots, reason := reserve(opts, 8)

	if jobs != 2 || len(slots) != 2 || reason != "memory pressure 80% after timeout" {
		t.Fatalf("got %d jobs, %d slots, %q", jobs, len(slots), reason)
	}
}

func TestReserveReadsItsOwnCgroupBeforeTheMachine(t *testing.T) {
	cgroupDir, pressureDir := t.TempDir(), t.TempDir()
	writePressure(t, cgroupDir, "cpu.pressure", "2.00")
	writePressure(t, pressureDir, "cpu", "95.00")
	opts := options{minJobs: 1, maxJobs: 4, pressureMaxPct: 25, timeout: 0, slotDir: t.TempDir(), cgroupDir: cgroupDir, pressureDir: pressureDir}

	jobs, _, reason := reserve(opts, 4)

	if jobs != 4 || reason != "free slots" {
		t.Fatalf("got %d jobs, %q; want 4, free slots", jobs, reason)
	}
}

func TestReserveIgnoresIOPressureUnlessAsked(t *testing.T) {
	pressureDir := t.TempDir()
	writePressure(t, pressureDir, "io", "90.00")
	opts := options{minJobs: 1, maxJobs: 4, pressureMaxPct: 25, timeout: 0, slotDir: t.TempDir(), pressureDir: pressureDir}

	ignored, ignoredSlots, _ := reserve(opts, 4)
	release(ignoredSlots)
	opts.watchIO = true
	watched, _, _ := reserve(opts, 4)

	if ignored != 4 || watched != 1 {
		t.Fatalf("got %d without -io, %d with; want 4, 1", ignored, watched)
	}
}

func TestReserveRunsWithMinWhenNoSlotIsFree(t *testing.T) {
	slotDir := t.TempDir()
	busy := acquire(slotDir, 2, 2)
	defer release(busy)
	opts := options{minJobs: 1, maxJobs: 2, pressureMaxPct: 25, timeout: 0, slotDir: slotDir, pressureDir: t.TempDir()}

	jobs, slots, reason := reserve(opts, 2)

	if jobs != 1 || len(slots) != 0 || reason != "no free slot after timeout" {
		t.Fatalf("got %d jobs, %d slots, %q", jobs, len(slots), reason)
	}
}

func TestSubstitute(t *testing.T) {
	got := substitute([]string{"run", "--maxWorkers={jobs}", "-j", "{jobs}"}, 8)

	want := []string{"run", "--maxWorkers=8", "-j", "8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v; want %v", got, want)
	}
}
