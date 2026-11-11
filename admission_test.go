package main

import "testing"

func TestBytesPerJobKeepsTheHeaviestRecentRun(t *testing.T) {
	runs := []profileRun{{Jobs: 4, PeakBytes: 4 * gb}, {Jobs: 2, PeakBytes: 6 * gb}, {Jobs: 4, PeakBytes: 8 * gb}}

	if got := bytesPerJob(runs); got != 3*gb {
		t.Fatalf("got %d; want %d", got, 3*gb)
	}
}

func TestBytesPerJobOfACommandNeverRun(t *testing.T) {
	if got := bytesPerJob(nil); got != 0 {
		t.Fatalf("got %d; want 0", got)
	}
}

func TestJobsThatFit(t *testing.T) {
	cases := []struct {
		name        string
		slots       int
		mem         memory
		perJobBytes int64
		margin      float64
		want        int
	}{
		{"memory limits a known command", 12, memory{availableBytes: 10 * gb, totalBytes: 32 * gb}, 2 * gb, 1.2, 2},
		{"slots limit a light command", 4, memory{availableBytes: 24 * gb, totalBytes: 32 * gb}, gb / 2, 1.2, 4},
		{"an unknown command gets the slots", 12, memory{availableBytes: 10 * gb, totalBytes: 32 * gb}, 0, 1.2, 12},
		{"nothing fits under the reserve", 12, memory{availableBytes: 3 * gb, totalBytes: 32 * gb}, 0, 1.2, 0},
		{"unreadable memory leaves the slots", 12, memory{}, 2 * gb, 1.2, 12},
		{"a wider margin fits fewer jobs", 12, memory{availableBytes: 10 * gb, totalBytes: 32 * gb}, 2 * gb, 3, 1},
	}
	for _, tc := range cases {
		if got := jobsThatFit(tc.slots, tc.mem, tc.perJobBytes, tc.margin); got != tc.want {
			t.Errorf("%s: got %d; want %d", tc.name, got, tc.want)
		}
	}
}

func TestRampStable(t *testing.T) {
	cases := []struct {
		name    string
		samples []int64
		want    bool
	}{
		{"too few samples", []int64{gb, gb, gb}, false},
		{"still growing", []int64{gb, 2 * gb, 3 * gb, 4 * gb}, false},
		{"flat", []int64{gb, 4 * gb, 4 * gb, 4 * gb, 4 * gb}, true},
		{"nothing measured", []int64{0, 0, 0, 0}, false},
	}
	for _, tc := range cases {
		if got := rampStable(tc.samples); got != tc.want {
			t.Errorf("%s: got %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestNextMargin(t *testing.T) {
	if got := nextMargin(1.2, true); got != 1.5 {
		t.Errorf("after a starved run: got %v; want 1.5", got)
	}
	if got := nextMargin(2.9, true); got != 3 {
		t.Errorf("capped: got %v; want 3", got)
	}
	if got := nextMargin(1.5, false); got != 1.48 {
		t.Errorf("after a quiet run: got %v; want 1.48", got)
	}
	if got := nextMargin(1.1, false); got != 1.1 {
		t.Errorf("floor: got %v; want 1.1", got)
	}
}

func TestStarving(t *testing.T) {
	if !starving(memory{availableBytes: 2 * gb, totalBytes: 32 * gb}) {
		t.Error("2 GB left of 32 is under the reserve")
	}
	if starving(memory{availableBytes: 8 * gb, totalBytes: 32 * gb}) || starving(memory{}) {
		t.Error("8 GB left of 32, or unreadable memory, is not starving")
	}
}
