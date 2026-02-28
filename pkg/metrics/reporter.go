package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// countWriter wraps an io.Writer and counts total bytes written.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// WriteTo writes the formatted latency report to w.
// This produces the pretty-printed CLI table shown after every build.
// Implements io.WriterTo.
func (t *Tracker) WriteTo(w io.Writer) (int64, error) {
	cw := &countWriter{w: w}
	const (
		colPhase = 22
		colVal   = 10
	)

	totalDur := t.BuildDuration()

	fmt.Fprintf(cw, "\n✅ Build completed in %s\n\n", formatDuration(totalDur))

	// Header
	line := strings.Repeat("─", colPhase+colVal*4+5)
	fmt.Fprintf(cw, "┌%s┐\n", line)
	fmt.Fprintf(cw, "│%s│\n", center("LATENCY BREAKDOWN", colPhase+colVal*4+5))
	fmt.Fprintf(cw, "├%s┬%s┬%s┬%s┬%s┤\n",
		strings.Repeat("─", colPhase),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal))
	fmt.Fprintf(cw, "│%-*s│%*s│%*s│%*s│%*s│\n",
		colPhase, " Phase",
		colVal, "Avg ",
		colVal, "p50 ",
		colVal, "p95 ",
		colVal, "p99 ")
	fmt.Fprintf(cw, "├%s┼%s┼%s┼%s┼%s┤\n",
		strings.Repeat("─", colPhase),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal))

	// Phase rows
	for _, name := range t.PhaseNames() {
		stats := t.PhaseStats(name)
		if stats == nil {
			continue
		}
		fmt.Fprintf(cw, "│ %-*s│%*s│%*s│%*s│%*s│\n",
			colPhase-1, truncate(name, colPhase-2),
			colVal, formatDuration(stats.Avg)+" ",
			colVal, formatDuration(stats.P50)+" ",
			colVal, formatDuration(stats.P95)+" ",
			colVal, formatDuration(stats.P99)+" ")

		// Sub-phases
		subNames := t.SubPhaseNames(name)
		for i, subName := range subNames {
			subStats := t.SubPhaseStats(name, subName)
			if subStats == nil {
				continue
			}
			prefix := "├─"
			if i == len(subNames)-1 {
				prefix = "└─"
			}
			label := fmt.Sprintf("  %s %s", prefix, subName)
			fmt.Fprintf(cw, "│ %-*s│%*s│%*s│%*s│%*s│\n",
				colPhase-1, truncate(label, colPhase-2),
				colVal, formatDuration(subStats.Avg)+" ",
				colVal, formatDuration(subStats.P50)+" ",
				colVal, formatDuration(subStats.P95)+" ",
				colVal, formatDuration(subStats.P99)+" ")
		}
	}

	// Total row
	fmt.Fprintf(cw, "│%s┼%s┼%s┼%s┼%s│\n",
		" "+strings.Repeat("─", colPhase-1),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal))
	fmt.Fprintf(cw, "│ %-*s│%*s│%*s│%*s│%*s│\n",
		colPhase-1, "Total",
		colVal, formatDuration(totalDur)+" ",
		colVal, "- ",
		colVal, "- ",
		colVal, "- ")
	fmt.Fprintf(cw, "└%s┴%s┴%s┴%s┴%s┘\n",
		strings.Repeat("─", colPhase),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal),
		strings.Repeat("─", colVal))

	// Memory stats
	startMem, endMem := t.MemoryStats()
	peakMB := float64(endMem.HeapAlloc) / (1024 * 1024)
	gcCount := endMem.NumGC - startMem.NumGC
	maxPauseMs := float64(endMem.MaxPauseNs) / 1e6
	allocs := endMem.NumAllocs - startMem.NumAllocs

	fmt.Fprintf(cw, "\nMemory: peak=%.0fMB, allocs=%d, GC pauses=%d (max %.1fms)\n",
		peakMB, allocs, gcCount, maxPauseMs)

	return cw.n, nil
}

// WriteJSON writes metrics as JSON to the given writer, suitable for
// export and analysis in external tools.
func (t *Tracker) WriteJSON(w io.Writer) error {
	report := MetricsReport{
		BuildDuration: t.BuildDuration().String(),
		Phases:        make([]PhaseReport, 0, len(t.order)),
	}

	for _, name := range t.PhaseNames() {
		stats := t.PhaseStats(name)
		if stats == nil {
			continue
		}

		pr := PhaseReport{
			Name:  name,
			Stats: statsToJSON(stats),
		}

		subNames := t.SubPhaseNames(name)
		for _, subName := range subNames {
			subStats := t.SubPhaseStats(name, subName)
			if subStats != nil {
				pr.SubPhases = append(pr.SubPhases, PhaseReport{
					Name:  subName,
					Stats: statsToJSON(subStats),
				})
			}
		}

		report.Phases = append(report.Phases, pr)
	}

	startMem, endMem := t.MemoryStats()
	report.Memory = MemoryReport{
		PeakHeapBytes: endMem.HeapAlloc,
		TotalAllocs:   endMem.NumAllocs - startMem.NumAllocs,
		GCPauses:      endMem.NumGC - startMem.NumGC,
		MaxPauseNs:    endMem.MaxPauseNs,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// ---------------------------------------------------------------------------
// JSON types
// ---------------------------------------------------------------------------

// MetricsReport is the top-level JSON structure for metrics export.
type MetricsReport struct {
	BuildDuration string        `json:"build_duration"`
	Phases        []PhaseReport `json:"phases"`
	Memory        MemoryReport  `json:"memory"`
}

// PhaseReport holds stats for one phase in JSON format.
type PhaseReport struct {
	Name      string        `json:"name"`
	Stats     StatsJSON     `json:"stats"`
	SubPhases []PhaseReport `json:"sub_phases,omitempty"`
}

// StatsJSON is the JSON-serializable form of Stats.
type StatsJSON struct {
	Count int    `json:"count"`
	Avg   string `json:"avg"`
	P50   string `json:"p50"`
	P95   string `json:"p95"`
	P99   string `json:"p99"`
	Min   string `json:"min"`
	Max   string `json:"max"`
}

// MemoryReport holds memory statistics for JSON export.
type MemoryReport struct {
	PeakHeapBytes uint64 `json:"peak_heap_bytes"`
	TotalAllocs   uint64 `json:"total_allocs"`
	GCPauses      uint32 `json:"gc_pauses"`
	MaxPauseNs    uint64 `json:"max_pause_ns"`
}

func statsToJSON(s *Stats) StatsJSON {
	return StatsJSON{
		Count: s.Count,
		Avg:   s.Avg.String(),
		P50:   s.P50.String(),
		P95:   s.P95.String(),
		P99:   s.P99.String(),
		Min:   s.Min.String(),
		Max:   s.Max.String(),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func center(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	pad := (width - len(s)) / 2
	return strings.Repeat(" ", pad) + s + strings.Repeat(" ", width-len(s)-pad)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// ---------------------------------------------------------------------------
// NoopTracker — used when metrics are disabled
// ---------------------------------------------------------------------------

// NoopTracker is a no-op implementation of MetricsTracker.
type NoopTracker struct{}

func (NoopTracker) StartPhase(string)                {}
func (NoopTracker) EndPhase(string)                  {}
func (NoopTracker) StartSubPhase(string, string)     {}
func (NoopTracker) EndSubPhase(string, string)       {}
func (NoopTracker) RecordAlloc()                     {}
func (NoopTracker) WriteTo(io.Writer) (int64, error) { return 0, nil }
func (NoopTracker) WriteJSON(io.Writer) error        { return nil }

// ---------------------------------------------------------------------------
// Bench helper — run build function N times and aggregate metrics
// ---------------------------------------------------------------------------

// BenchRun executes fn N times and merges timings from each run's tracker
// into a single aggregate tracker. Each fn receives its own fresh tracker.
func BenchRun(n int, fn func(t *Tracker) error) (*Tracker, error) {
	agg := NewTracker()
	agg.buildStart = time.Now()

	for i := 0; i < n; i++ {
		run := NewTracker()
		if err := fn(run); err != nil {
			return nil, fmt.Errorf("bench run %d: %w", i, err)
		}

		// Merge run timings into aggregate
		run.mu.Lock()
		for name, pt := range run.phases {
			agg.mu.Lock()
			if _, ok := agg.phases[name]; !ok {
				agg.phases[name] = &PhaseTiming{
					Name:      name,
					Durations: make([]time.Duration, 0, n),
				}
				agg.order = append(agg.order, name)
			}
			agg.phases[name].Durations = append(agg.phases[name].Durations, pt.Durations...)
			agg.mu.Unlock()
		}
		for key, sp := range run.subPhases {
			agg.mu.Lock()
			if _, ok := agg.subPhases[key]; !ok {
				agg.subPhases[key] = &SubPhaseTiming{
					Name:      sp.Name,
					Parent:    sp.Parent,
					Durations: make([]time.Duration, 0, n),
				}
				if _, exists := agg.subOrder[sp.Parent]; !exists {
					agg.subOrder[sp.Parent] = []string{}
				}
				found := false
				for _, existing := range agg.subOrder[sp.Parent] {
					if existing == sp.Name {
						found = true
						break
					}
				}
				if !found {
					agg.subOrder[sp.Parent] = append(agg.subOrder[sp.Parent], sp.Name)
				}
			}
			agg.subPhases[key].Durations = append(agg.subPhases[key].Durations, sp.Durations...)
			agg.mu.Unlock()
		}
		run.mu.Unlock()
	}

	agg.buildEnd = time.Now()
	agg.endMem = captureMemStats()
	return agg, nil
}
