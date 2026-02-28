package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestTrackerBasic(t *testing.T) {
	tracker := NewTracker()
	tracker.StartBuild()

	// Simulate a phase
	tracker.StartPhase("Parse")
	time.Sleep(1 * time.Millisecond)
	tracker.EndPhase("Parse")

	tracker.StartPhase("Execute")
	time.Sleep(2 * time.Millisecond)
	tracker.EndPhase("Execute")

	tracker.EndBuild()

	// Check phases recorded
	names := tracker.PhaseNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(names))
	}
	if names[0] != "Parse" || names[1] != "Execute" {
		t.Errorf("unexpected phase names: %v", names)
	}

	// Check stats
	stats := tracker.PhaseStats("Parse")
	if stats == nil {
		t.Fatal("Parse stats is nil")
	}
	if stats.Count != 1 {
		t.Errorf("expected count=1, got %d", stats.Count)
	}
	if stats.Avg < time.Millisecond {
		t.Errorf("Parse avg too low: %v", stats.Avg)
	}
}

func TestTrackerSubPhases(t *testing.T) {
	tracker := NewTracker()
	tracker.StartBuild()

	tracker.StartPhase("Snapshot")
	tracker.StartSubPhase("Snapshot", "Diff")
	time.Sleep(1 * time.Millisecond)
	tracker.EndSubPhase("Snapshot", "Diff")
	tracker.StartSubPhase("Snapshot", "Compress")
	time.Sleep(1 * time.Millisecond)
	tracker.EndSubPhase("Snapshot", "Compress")
	tracker.EndPhase("Snapshot")

	tracker.EndBuild()

	subNames := tracker.SubPhaseNames("Snapshot")
	if len(subNames) != 2 {
		t.Fatalf("expected 2 sub-phases, got %d", len(subNames))
	}
	if subNames[0] != "Diff" || subNames[1] != "Compress" {
		t.Errorf("unexpected sub-phase names: %v", subNames)
	}

	diffStats := tracker.SubPhaseStats("Snapshot", "Diff")
	if diffStats == nil {
		t.Fatal("Diff sub-phase stats is nil")
	}
	if diffStats.Count != 1 {
		t.Error("expected Diff count=1")
	}
}

func TestPercentileComputation(t *testing.T) {
	durations := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
		6 * time.Millisecond,
		7 * time.Millisecond,
		8 * time.Millisecond,
		9 * time.Millisecond,
		10 * time.Millisecond,
	}

	stats := computeStats(durations)
	if stats.P50 != 5*time.Millisecond {
		t.Errorf("p50: expected 5ms, got %v", stats.P50)
	}
	if stats.P99 != 9*time.Millisecond {
		t.Errorf("p99: expected 9ms, got %v", stats.P99)
	}
	expected := 5500 * time.Microsecond
	if stats.Avg != expected {
		t.Errorf("avg: expected %v, got %v", expected, stats.Avg)
	}
}

func TestReporterWriteTo(t *testing.T) {
	tracker := NewTracker()
	tracker.StartBuild()

	tracker.StartPhase("Parse")
	time.Sleep(1 * time.Millisecond)
	tracker.EndPhase("Parse")

	tracker.StartPhase("Snapshot")
	tracker.StartSubPhase("Snapshot", "Diff")
	time.Sleep(1 * time.Millisecond)
	tracker.EndSubPhase("Snapshot", "Diff")
	tracker.EndPhase("Snapshot")

	tracker.EndBuild()
	tracker.RecordAlloc()

	var buf bytes.Buffer
	_, err := tracker.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "LATENCY BREAKDOWN") {
		t.Error("output missing LATENCY BREAKDOWN header")
	}
	if !strings.Contains(output, "Parse") {
		t.Error("output missing Parse phase")
	}
	if !strings.Contains(output, "Diff") {
		t.Error("output missing Diff sub-phase")
	}
	if !strings.Contains(output, "Memory:") {
		t.Error("output missing Memory stats")
	}
}

func TestReporterJSON(t *testing.T) {
	tracker := NewTracker()
	tracker.StartBuild()
	tracker.StartPhase("Parse")
	time.Sleep(1 * time.Millisecond)
	tracker.EndPhase("Parse")
	tracker.EndBuild()

	var buf bytes.Buffer
	err := tracker.WriteJSON(&buf)
	if err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `"name": "Parse"`) {
		t.Error("JSON output missing Parse phase")
	}
	if !strings.Contains(output, `"build_duration"`) {
		t.Error("JSON output missing build_duration")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Nanosecond, "500ns"},
		{1500 * time.Nanosecond, "1.5µs"},
		{1500 * time.Microsecond, "1.5ms"},
		{1500 * time.Millisecond, "1.50s"},
	}
	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func BenchmarkTrackerStartEnd(b *testing.B) {
	tracker := NewTracker()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.StartPhase("test")
		tracker.EndPhase("test")
	}
}
