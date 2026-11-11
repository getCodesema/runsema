package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const bytesPerKB = 1024

// readMemory is what the process may still use: the tightest memory limit set on its cgroup or on one
// above it (a container, a systemd scope or slice), else the machine. A zero total means it could not
// be read.
func readMemory(opts options) memory {
	mem := memory{
		availableBytes: meminfoBytes(opts.procDir, "MemAvailable"),
		totalBytes:     meminfoBytes(opts.procDir, "MemTotal"),
	}
	for _, dir := range ownCgroupDirs(opts) {
		limitBytes, err := readInt(filepath.Join(dir, "memory.max"))
		if err != nil {
			continue
		}
		availableBytes := max(0, limitBytes-cgroupUsedBytes(dir))
		if mem.totalBytes <= 0 || availableBytes < mem.availableBytes {
			mem = memory{availableBytes: availableBytes, totalBytes: limitBytes}
		}
	}
	return mem
}

// ownCgroupDirs lists the cgroup of the process and every cgroup above it, nearest first. Inside a
// container the process sees its own cgroup as the root, so the list is that single directory.
func ownCgroupDirs(opts options) []string {
	relative := "/"
	if content, err := os.ReadFile(filepath.Join(opts.procDir, "self", "cgroup")); err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			if path, found := strings.CutPrefix(line, "0::"); found {
				relative = strings.TrimSpace(path)
			}
		}
	}
	root := filepath.Clean(opts.cgroupDir)
	var dirs []string
	for dir := filepath.Join(root, relative); strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == root {
			break
		}
	}
	return dirs
}

// cgroupUsedBytes counts what cannot be reclaimed: memory.current also holds the page cache, which the
// kernel gives back on demand.
func cgroupUsedBytes(cgroupDir string) int64 {
	content, err := os.ReadFile(filepath.Join(cgroupDir, "memory.stat"))
	if err != nil {
		usedBytes, _ := readInt(filepath.Join(cgroupDir, "memory.current"))
		return usedBytes
	}
	var usedBytes int64
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[0] == "anon" || fields[0] == "shmem") {
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			usedBytes += value
		}
	}
	return usedBytes
}

func meminfoBytes(procDir string, field string) int64 {
	return kbField(filepath.Join(procDir, "meminfo"), field) * bytesPerKB
}

// kbField reads a "Name:   123 kB" line, the format of /proc/meminfo and /proc/<pid>/status.
func kbField(path string, field string) int64 {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(content), "\n") {
		if rest, found := strings.CutPrefix(line, field+":"); found {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				value, _ := strconv.ParseInt(fields[0], 10, 64)
				return value
			}
		}
	}
	return 0
}

func readInt(path string) (int64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
}

// treeAnonBytes is the anonymous memory of a process and all its descendants: what the command really
// costs, without the files it maps.
func treeAnonBytes(procDir string, root int) int64 {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return 0
	}
	children := map[int][]int{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		children[parentOf(procDir, pid)] = append(children[parentOf(procDir, pid)], pid)
	}
	var totalBytes int64
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = append(queue[1:], children[pid]...)
		totalBytes += kbField(filepath.Join(procDir, strconv.Itoa(pid), "status"), "RssAnon") * bytesPerKB
	}
	return totalBytes
}

// parentOf reads the parent pid in /proc/<pid>/stat. The command name sits in parentheses and may hold
// spaces, so the fields are counted from the last closing one.
func parentOf(procDir string, pid int) int {
	content, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return -1
	}
	fields := strings.Fields(string(content)[strings.LastIndexByte(string(content), ')')+1:])
	if len(fields) < 2 {
		return -1
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}
	return parent
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
	return filepath.Join(opts.procDir, "pressure", resource)
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
