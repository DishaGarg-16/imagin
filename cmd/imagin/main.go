// imagin is a Docker image builder optimised for p95/p99 latency.
//
// Usage:
//
//	imagin build -f Dockerfile -o output/ .
//	imagin build -f Dockerfile -o output/ --bench=10 .
//	imagin build -f Dockerfile -o output.tar --format=tar .
//	imagin build -f Dockerfile -o output/ --metrics-json=metrics.json .
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/executor"
	"github.com/imagin/imagin/pkg/exporter"
	"github.com/imagin/imagin/pkg/metadata"
	"github.com/imagin/imagin/pkg/metrics"
	"github.com/imagin/imagin/pkg/parser"
)

func main() {
	// Define CLI flags
	dockerfile := flag.String("f", "Dockerfile", "Path to the Dockerfile")
	outputPath := flag.String("o", "./output", "Output path (directory for OCI layout, file for tar)")
	format := flag.String("format", "oci", "Output format: 'oci' (directory) or 'tar' (single file)")
	benchN := flag.Int("bench", 0, "Run build N times and report aggregate latency percentiles")
	metricsJSON := flag.String("metrics-json", "", "Export metrics to JSON file")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: imagin build [options] <context-dir>\n\n")
		fmt.Fprintf(os.Stderr, "IMAGIN — Docker image builder optimised for p95/p99 latency\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  imagin build -f Dockerfile -o ./output .\n")
		fmt.Fprintf(os.Stderr, "  imagin build -f Dockerfile -o image.tar --format=tar .\n")
		fmt.Fprintf(os.Stderr, "  imagin build -f Dockerfile -o ./output --bench=100 .\n")
	}

	flag.Parse()

	// Determine build context directory
	contextDir := "."
	if flag.NArg() > 0 {
		// Skip "build" subcommand if present
		args := flag.Args()
		if args[0] == "build" {
			args = args[1:]
		}
		if len(args) > 0 {
			contextDir = args[len(args)-1]
		}
	}

	// Set up signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Run the build
	if *benchN > 0 {
		runBenchmark(ctx, *dockerfile, contextDir, *outputPath, *format, *benchN, *metricsJSON)
	} else {
		tracker := metrics.NewTracker()
		if err := runBuild(ctx, *dockerfile, contextDir, *outputPath, *format, tracker); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Build failed: %v\n", err)
			os.Exit(1)
		}

		// Print latency report
		tracker.WriteTo(os.Stdout)

		// Export metrics JSON if requested
		if *metricsJSON != "" {
			f, err := os.Create(*metricsJSON)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create metrics file: %v\n", err)
			} else {
				tracker.WriteJSON(f)
				f.Close()
				fmt.Fprintf(os.Stdout, "Metrics exported to %s\n", *metricsJSON)
			}
		}
	}
}

// runBuild executes a single build from Dockerfile to OCI output.
func runBuild(ctx context.Context, dockerfilePath, contextDir, outputPath, format string, tracker *metrics.Tracker) error {
	tracker.StartBuild()
	defer func() {
		tracker.EndBuild()
		tracker.RecordAlloc()
	}()

	// Phase 1: Parse Dockerfile
	tracker.StartPhase("Dockerfile Parse")
	source, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return fmt.Errorf("read Dockerfile: %w", err)
	}

	df, err := parser.Parse(string(source))
	if err != nil {
		return fmt.Errorf("parse Dockerfile: %w", err)
	}
	tracker.EndPhase("Dockerfile Parse")

	fmt.Printf("📋 Parsed %d stage(s), %d total instructions\n",
		len(df.Stages), countInstructions(df))

	// Create a temp work directory
	workDir, err := os.MkdirTemp("", "imagin-build-*")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	// Phase 2: Execute all stages
	store := metadata.NewStore()
	absContext, _ := filepath.Abs(contextDir)

	exec, err := executor.New(executor.Config{
		BuildContext: absContext,
		WorkDir:      workDir,
		Store:        store,
		Tracker:      tracker,
	})
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}
	defer exec.Cleanup()

	for i, stage := range df.Stages {
		fmt.Printf("🔨 Stage %d/%d: %s\n", i+1, len(df.Stages), stageName(&stage))
		if err := exec.ExecuteStage(ctx, &stage); err != nil {
			return fmt.Errorf("stage %d: %w", i, err)
		}
	}

	// Phase 3: Export
	tracker.StartPhase("OCI Export")

	config := exec.BuildConfig()
	layers := store.GetLayers()
	store.SetConfig(config)

	var exp interface {
		Export(ctx context.Context, config interface{}, layers interface{}, outputPath string) error
	}
	_ = exp // We'll use the concrete types below

	switch format {
	case "tar":
		tarExp := exporter.NewTarExporter()
		err = tarExp.Export(ctx, config, layers, outputPath)
	default:
		ociExp := exporter.NewOCIExporter()
		err = ociExp.Export(ctx, config, layers, outputPath)
	}
	tracker.EndPhase("OCI Export")

	if err != nil {
		return fmt.Errorf("export: %w", err)
	}

	layerCount := 0
	var totalSize int64
	for _, l := range layers {
		if l.Size > 0 {
			layerCount++
			totalSize += l.Size
		}
	}
	fmt.Printf("📦 Exported %d layer(s), %.1f KB total compressed\n",
		layerCount, float64(totalSize)/1024)

	return nil
}

// runBenchmark runs the build N times and reports aggregate metrics.
func runBenchmark(ctx context.Context, dockerfilePath, contextDir, outputPath, format string, n int, metricsJSONPath string) {
	fmt.Printf("🏁 Running benchmark: %d iterations\n\n", n)

	start := time.Now()
	agg, err := metrics.BenchRun(n, func(tracker *metrics.Tracker) error {
		return runBuild(ctx, dockerfilePath, contextDir, outputPath, format, tracker)
	})
	elapsed := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Benchmark failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n🏁 Benchmark complete: %d iterations in %s (avg %.2fs/build)\n",
		n, elapsed, elapsed.Seconds()/float64(n))

	agg.WriteTo(os.Stdout)

	if metricsJSONPath != "" {
		f, err := os.Create(metricsJSONPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create metrics file: %v\n", err)
			return
		}
		agg.WriteJSON(f)
		f.Close()
		fmt.Printf("Metrics exported to %s\n", metricsJSONPath)
	}
}

func countInstructions(df *types.Dockerfile) int {
	total := len(df.GlobalArgs)
	for _, stage := range df.Stages {
		total += len(stage.Instructions)
	}
	return total
}

func stageName(stage *types.BuildStage) string {
	if stage.Name != "" {
		return fmt.Sprintf("%s:%s (as %s)", stage.BaseName, stage.BaseTag, stage.Name)
	}
	return fmt.Sprintf("%s:%s", stage.BaseName, stage.BaseTag)
}
