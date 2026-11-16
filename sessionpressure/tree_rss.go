package sessionpressure

import (
	"sync"
	"time"
)

// Process-tree peak memory of a leased command (work queue v2, capacity
// path, 2026-11-16).
//
// wait4 reports the largest single process of a command. `go test` and Swift
// builds run many processes at once, so their real peak is the sum over the
// tree. The work-run process samples its child's tree once a second and
// records the largest sum as command_tree_peak_rss_mb. The memory-headroom
// predictor uses the larger of the two values.

const treeRSSSampleInterval = time.Second

type processRow struct {
	pid, ppid int
	rssKB     int64
}

// treeRSSKB sums the resident memory of root and all its descendants. A
// process whose parent is unknown is not attributed. rssOf, when set, reads
// memory for tree members only; otherwise the row's rssKB is used.
func treeRSSKB(rows []processRow, root int, rssOf func(pid int) (int64, bool)) (int64, int) {
	children := make(map[int][]int, len(rows))
	rss := make(map[int]int64, len(rows))
	for _, row := range rows {
		children[row.ppid] = append(children[row.ppid], row.pid)
		rss[row.pid] = row.rssKB
	}
	if _, ok := rss[root]; !ok {
		return 0, 0
	}
	total, count := int64(0), 0
	seen := map[int]bool{}
	stack := []int{root}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		kb := rss[pid]
		if rssOf != nil {
			if measured, ok := rssOf(pid); ok {
				kb = measured
			}
		}
		total += kb
		count++
		stack = append(stack, children[pid]...)
	}
	return total, count
}

// treePeakSampler samples one process tree until stopped.
type treePeakSampler struct {
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
	peakKB   int64
	samples  int
}

// startTreePeakSampler returns nil when the platform cannot list processes.
func startTreePeakSampler(root int, interval time.Duration, list func() ([]processRow, error), rssOf func(pid int) (int64, bool)) *treePeakSampler {
	if root <= 0 || list == nil {
		return nil
	}
	sampler := &treePeakSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(sampler.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if rows, err := list(); err == nil {
				if kb, count := treeRSSKB(rows, root, rssOf); count > 0 {
					sampler.peakKB = max(sampler.peakKB, kb)
					sampler.samples++
				}
			}
			select {
			case <-sampler.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return sampler
}

// Stop ends sampling and returns the peak in MB and the sample count.
func (sampler *treePeakSampler) Stop() (float64, int) {
	if sampler == nil {
		return 0, 0
	}
	sampler.stopOnce.Do(func() { close(sampler.stop) })
	<-sampler.done
	return float64(sampler.peakKB) / 1024, sampler.samples
}
