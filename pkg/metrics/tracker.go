// Package metrics provides build-phase instrumentation with percentile
// latency computation. Designed for zero allocation on the timing hot path.
package metrics

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

// PhaseTiming stores all recorded durations for a single phase.
type PhaseTiming struct {
	Name      string
	Durations []time.Duration
	start     time.Time
}

// SubPhaseTiming stores sub-phase timings within a parent phase.
type SubPhaseTiming struct {
	Name      string
	Parent    string
	Durations []time.Duration
	start     time.Time
}

// MemSnapshot captures Go runtime memory statistics.
type MemSnapshot struct {
	HeapAlloc    uint64 // current heap allocation
	TotalAlloc   uint64 // cumulative allocation
	NumGC        uint32 // total GC cycles
	PauseTotalNs uint64 // total GC pause time
	MaxPauseNs   uint64 // longest single GC pause
	NumAllocs    uint64 // total number of heap allocations
}

// Tracker instruments build phases for latency measurement.
// Implements the types.MetricsTracker interface.
type Tracker struct {
	mu        sync.Mutex
	phases    map[string]*PhaseTiming
	subPhases map[string]*SubPhaseTiming // "parent.child" → timing
	order     []string                   // insertion order
	subOrder  map[string][]string        // parent → [child names]

	startMem   MemSnapshot
	endMem     MemSnapshot
	buildStart time.Time
	buildEnd   time.Time
}

// NewTracker creates a new Tracker with pre-allocated storage.
func NewTracker() *Tracker {
	return &Tracker{
		phases:    make(map[string]*PhaseTiming, 16),
		subPhases: make(map[string]*SubPhaseTiming, 16),
		order:     make([]string, 0, 16),
		subOrder:  make(map[string][]string, 8),
	}
}

// StartBuild records the beginning of the overall build.
func (t *Tracker) StartBuild() {
	t.buildStart = time.Now()
	t.startMem = captureMemStats()
}

// EndBuild records the end of the overall build.
func (t *Tracker) EndBuild() {
	t.buildEnd = time.Now()
	t.endMem = captureMemStats()
}

// StartPhase begins timing a named phase.
func (t *Tracker) StartPhase(name string) {
	t.mu.Lock()
	pt, ok := t.phases[name]
	if !ok {
		pt = &PhaseTiming{
			Name:      name,
			Durations: make([]time.Duration, 0, 64), // pre-alloc for bench mode
		}
		t.phases[name] = pt
		t.order = append(t.order, name)
	}
	pt.start = time.Now()
	t.mu.Unlock()
}

// EndPhase stops timing the named phase and records the duration.
func (t *Tracker) EndPhase(name string) {
	end := time.Now() // capture time before locking
	t.mu.Lock()
	if pt, ok := t.phases[name]; ok {
		pt.Durations = append(pt.Durations, end.Sub(pt.start))
	}
	t.mu.Unlock()
}

// StartSubPhase begins timing a sub-phase within the given parent.
func (t *Tracker) StartSubPhase(parent string, name string) {
	key := parent + "." + name
	t.mu.Lock()
	sp, ok := t.subPhases[key]
	if !ok {
		sp = &SubPhaseTiming{
			Name:      name,
			Parent:    parent,
			Durations: make([]time.Duration, 0, 64),
		}
		t.subPhases[key] = sp
		t.subOrder[parent] = append(t.subOrder[parent], name)
	}
	sp.start = time.Now()
	t.mu.Unlock()
}

// EndSubPhase stops timing a sub-phase.
func (t *Tracker) EndSubPhase(parent string, name string) {
	end := time.Now()
	key := parent + "." + name
	t.mu.Lock()
	if sp, ok := t.subPhases[key]; ok {
		sp.Durations = append(sp.Durations, end.Sub(sp.start))
	}
	t.mu.Unlock()
}

// RecordAlloc takes a snapshot of current memory statistics.
func (t *Tracker) RecordAlloc() {
	t.mu.Lock()
	t.endMem = captureMemStats()
	t.mu.Unlock()
}

// BuildDuration returns total build time.
func (t *Tracker) BuildDuration() time.Duration {
	return t.buildEnd.Sub(t.buildStart)
}

// PhaseNames returns phase names in insertion order.
func (t *Tracker) PhaseNames() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.order))
	copy(out, t.order)
	return out
}

// PhaseStats returns computed statistics for a given phase.
func (t *Tracker) PhaseStats(name string) *Stats {
	t.mu.Lock()
	pt, ok := t.phases[name]
	t.mu.Unlock()
	if !ok || len(pt.Durations) == 0 {
		return nil
	}
	return computeStats(pt.Durations)
}

// SubPhaseNames returns sub-phase names for the given parent.
func (t *Tracker) SubPhaseNames(parent string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.subOrder[parent]
}

// SubPhaseStats returns stats for a specific sub-phase.
func (t *Tracker) SubPhaseStats(parent, name string) *Stats {
	key := parent + "." + name
	t.mu.Lock()
	sp, ok := t.subPhases[key]
	t.mu.Unlock()
	if !ok || len(sp.Durations) == 0 {
		return nil
	}
	return computeStats(sp.Durations)
}

// MemoryStats returns the memory delta between build start and end.
func (t *Tracker) MemoryStats() (start, end MemSnapshot) {
	return t.startMem, t.endMem
}

// ---------------------------------------------------------------------------
// Statistics computation
// ---------------------------------------------------------------------------

// Stats holds computed percentile latency statistics.
type Stats struct {
	Count int
	Avg   time.Duration
	P50   time.Duration
	P95   time.Duration
	P99   time.Duration
	Min   time.Duration
	Max   time.Duration
}

// String returns a compact representation of the stats.
func (s *Stats) String() string {
	return fmt.Sprintf("avg=%s p50=%s p95=%s p99=%s (n=%d)",
		formatDuration(s.Avg), formatDuration(s.P50),
		formatDuration(s.P95), formatDuration(s.P99), s.Count)
}

// computeStats calculates percentiles from a slice of durations.
// Uses sorted-insertion for small N — no extra allocation needed.
func computeStats(durations []time.Duration) *Stats {
	n := len(durations)
	if n == 0 {
		return nil
	}

	// Copy to avoid mutating the original
	sorted := make([]time.Duration, n)
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var total time.Duration
	for _, d := range sorted {
		total += d
	}

	return &Stats{
		Count: n,
		Avg:   total / time.Duration(n),
		P50:   percentile(sorted, 0.50),
		P95:   percentile(sorted, 0.95),
		P99:   percentile(sorted, 0.99),
		Min:   sorted[0],
		Max:   sorted[n-1],
	}
}

// percentile returns the value at the given percentile (0.0–1.0).
func percentile(sorted []time.Duration, pct float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(pct * float64(len(sorted)-1))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// formatDuration formats a duration for human-readable display.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%.1fµs", float64(d.Nanoseconds())/1000)
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Nanoseconds())/1e6)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// captureMemStats reads Go runtime memory statistics.
func captureMemStats() MemSnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	var maxPause uint64
	for _, p := range m.PauseNs {
		if p > maxPause {
			maxPause = p
		}
	}

	return MemSnapshot{
		HeapAlloc:    m.HeapAlloc,
		TotalAlloc:   m.TotalAlloc,
		NumGC:        m.NumGC,
		PauseTotalNs: m.PauseTotalNs,
		MaxPauseNs:   maxPause,
		NumAllocs:    m.Mallocs,
	}
}
