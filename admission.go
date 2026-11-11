package main

// The decisions, as pure functions: how much a command weighs, how many jobs fit in memory, when a run
// has finished loading, how the safety margin moves.

const (
	defaultMargin = 1.2
	// The margin never goes under this: a command always gets room above what it was seen using.
	minMargin    = 1.1
	maxMargin    = 3.0
	marginGrowth = 1.25
	marginDecay  = 0.02
	// Share of the memory kept free whatever runs.
	reserveShare = 0.10
	keptRuns     = 10
	// A run has finished loading when its memory grew by less than this share over the last samples.
	rampStableShare   = 0.05
	rampStableSamples = 4
)

type profileRun struct {
	Jobs      int   `json:"jobs"`
	PeakBytes int64 `json:"peakBytes"`
}

type memory struct {
	availableBytes int64
	totalBytes     int64
}

// bytesPerJob is the most memory one job of a command used over its recorded runs, 0 when it never ran.
// The largest, not the average: a suite that grows counts from its next run, and one heavy run is
// remembered until it leaves the kept runs.
func bytesPerJob(runs []profileRun) int64 {
	var largestBytes int64
	for _, run := range runs {
		if run.Jobs > 0 {
			largestBytes = max(largestBytes, run.PeakBytes/int64(run.Jobs))
		}
	}
	return largestBytes
}

// jobsThatFit is how many of the free slots memory allows. A command never measured gets them all, as
// long as some memory is left above the reserve: it runs alone through the gate while it is measured.
func jobsThatFit(slots int, mem memory, perJobBytes int64, margin float64) int {
	if mem.totalBytes <= 0 {
		return slots
	}
	budgetBytes := float64(mem.availableBytes) - reserveShare*float64(mem.totalBytes)
	if budgetBytes <= 0 {
		return 0
	}
	if perJobBytes <= 0 {
		return slots
	}
	return min(slots, int(budgetBytes/(float64(perJobBytes)*margin)))
}

func starving(mem memory) bool {
	return mem.totalBytes > 0 && float64(mem.availableBytes) < reserveShare*float64(mem.totalBytes)
}

// rampStable tells whether the memory samples of a starting run have stopped growing.
func rampStable(samplesBytes []int64) bool {
	if len(samplesBytes) < rampStableSamples {
		return false
	}
	last := samplesBytes[len(samplesBytes)-1]
	before := samplesBytes[len(samplesBytes)-rampStableSamples]
	return last > 0 && float64(last-before) <= rampStableShare*float64(last)
}

// nextMargin widens the safety margin fast after a run that ran short of memory, and tightens it slowly
// after a run that did not.
func nextMargin(margin float64, starved bool) float64 {
	if starved {
		return min(maxMargin, margin*marginGrowth)
	}
	return max(minMargin, margin-marginDecay)
}
