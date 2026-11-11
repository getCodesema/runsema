package main

import (
	"path/filepath"
	"testing"
)

func TestReadPressure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu")
	writePressure(t, path, "42.50")

	pct, err := readPressure(path)

	if err != nil || pct != 42.5 {
		t.Fatalf("got %v, %v; want 42.5, nil", pct, err)
	}
}

func TestReadPressureMissingFile(t *testing.T) {
	if _, err := readPressure(filepath.Join(t.TempDir(), "cpu")); err == nil {
		t.Fatal("want an error when PSI is unavailable")
	}
}

func TestReadMemoryOfTheMachine(t *testing.T) {
	opts := newOptions(t)

	got := readMemory(opts)

	if got != (memory{availableBytes: 24 * gb, totalBytes: 32 * gb}) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadMemoryOfAContainerCountsWhatCannotBeReclaimed(t *testing.T) {
	opts := newOptions(t)
	writeFile(t, filepath.Join(opts.cgroupDir, "memory.max"), "8589934592\n")
	writeFile(t, filepath.Join(opts.cgroupDir, "memory.current"), "7516192768\n")
	writeFile(t, filepath.Join(opts.cgroupDir, "memory.stat"), "anon 2147483648\nfile 4294967296\nshmem 1073741824\n")

	got := readMemory(opts)

	if got != (memory{availableBytes: 5 * gb, totalBytes: 8 * gb}) {
		t.Fatalf("got %+v; want 5 GB available of 8", got)
	}
}

func TestReadMemoryOfAContainerWithoutLimitIsTheMachine(t *testing.T) {
	opts := newOptions(t)
	writeFile(t, filepath.Join(opts.cgroupDir, "memory.max"), "max\n")

	got := readMemory(opts)

	if got != (memory{availableBytes: 24 * gb, totalBytes: 32 * gb}) {
		t.Fatalf("got %+v", got)
	}
}

func TestTreeAnonBytesSumsTheCommandAndItsDescendants(t *testing.T) {
	procDir := t.TempDir()
	process := func(pid string, parent string, name string, anonKB string) {
		writeFile(t, filepath.Join(procDir, pid, "stat"), pid+" ("+name+") S "+parent+" 1 1 0\n")
		writeFile(t, filepath.Join(procDir, pid, "status"), "Name:\t"+name+"\nRssAnon:\t"+anonKB+" kB\nRssFile:\t999999 kB\n")
	}
	process("100", "1", "runner", "1024")
	process("101", "100", "worker (1)", "2048")
	process("102", "101", "grandchild", "4096")
	process("200", "1", "browser", "8192")

	got := treeAnonBytes(procDir, 100)

	if got != (1024+2048+4096)*1024 {
		t.Fatalf("got %d; want %d", got, (1024+2048+4096)*1024)
	}
}

func TestReadMemoryFollowsTheTightestLimitAboveTheProcess(t *testing.T) {
	opts := newOptions(t)
	writeFile(t, filepath.Join(opts.procDir, "self", "cgroup"), "0::/user.slice/run-1.scope\n")
	writeFile(t, filepath.Join(opts.cgroupDir, "user.slice", "memory.max"), "max\n")
	writeFile(t, filepath.Join(opts.cgroupDir, "user.slice", "run-1.scope", "memory.max"), "17179869184\n")
	writeFile(t, filepath.Join(opts.cgroupDir, "user.slice", "run-1.scope", "memory.stat"), "anon 6442450944\nshmem 0\n")

	got := readMemory(opts)

	if got != (memory{availableBytes: 10 * gb, totalBytes: 16 * gb}) {
		t.Fatalf("got %+v; want 10 GB available of 16", got)
	}
}
