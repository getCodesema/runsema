package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestRecordRunRemembersTheRunAndMovesTheMargin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "profiles.json")

	if err := recordRun(path, "key", profileRun{Jobs: 4, PeakBytes: 8 * gb}, true); err != nil {
		t.Fatal(err)
	}
	saved := loadStore(path)

	if !reflect.DeepEqual(saved.Commands["key"], []profileRun{{Jobs: 4, PeakBytes: 8 * gb}}) || saved.Margin != 1.5 {
		t.Fatalf("got %+v", saved)
	}
}

func TestRecordRunKeepsTheLastRunsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")

	for jobs := 1; jobs <= 12; jobs++ {
		if err := recordRun(path, "key", profileRun{Jobs: jobs, PeakBytes: gb}, false); err != nil {
			t.Fatal(err)
		}
	}
	runs := loadStore(path).Commands["key"]

	if len(runs) != 10 || runs[0].Jobs != 3 || runs[9].Jobs != 12 {
		t.Fatalf("got %d runs, from %d to %d; want 10, from 3 to 12", len(runs), runs[0].Jobs, runs[len(runs)-1].Jobs)
	}
}

func TestLoadStoreOfADamagedFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	writeFile(t, path, "{not json")

	saved := loadStore(path)

	if saved.Margin != 1.2 || len(saved.Commands) != 0 {
		t.Fatalf("got %+v", saved)
	}
}

func TestCommandKeyDependsOnTheDirectoryAndTheCommand(t *testing.T) {
	here := commandKey("/a", []string{"vitest", "run"})

	if here != commandKey("/a", []string{"vitest", "run"}) {
		t.Error("the same command in the same directory must keep its key")
	}
	if here == commandKey("/b", []string{"vitest", "run"}) || here == commandKey("/a", []string{"vitest", "run", "x"}) {
		t.Error("another directory or another command must have another key")
	}
}
