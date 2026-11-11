package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// store is what runsema remembers between runs: what each command used, and the safety margin.
type store struct {
	Margin   float64                 `json:"margin"`
	Commands map[string][]profileRun `json:"commands"`
}

// commandKey identifies a command by where it runs and how it is written, before {jobs} is replaced.
func commandKey(dir string, command []string) string {
	sum := sha256.Sum256([]byte(dir + "\x00" + strings.Join(command, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// loadStore never fails: a missing or damaged file is an empty memory.
func loadStore(path string) store {
	saved := store{}
	if content, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(content, &saved)
	}
	if saved.Margin < minMargin {
		saved.Margin = defaultMargin
	}
	if saved.Commands == nil {
		saved.Commands = map[string][]profileRun{}
	}
	return saved
}

// updateStore applies a change under a lock, so runs that end together do not lose each other's record.
func updateStore(path string, change func(*store)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	saved := loadStore(path)
	change(&saved)
	content, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func recordRun(path string, key string, run profileRun, starved bool) error {
	return updateStore(path, func(saved *store) {
		runs := append(saved.Commands[key], run)
		if len(runs) > keptRuns {
			runs = runs[len(runs)-keptRuns:]
		}
		saved.Commands[key] = runs
		saved.Margin = nextMargin(saved.Margin, starved)
	})
}
