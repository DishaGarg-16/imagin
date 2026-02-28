// Package executor orchestrates the Dockerfile build process, coordinating
// the parser, rootfs, snapshotter, metadata store, and metrics tracker.
package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/metadata"
	"github.com/imagin/imagin/pkg/metrics"
	"github.com/imagin/imagin/pkg/rootfs"
	"github.com/imagin/imagin/pkg/snapshotter"
)

// Executor orchestrates the image build process.
type Executor struct {
	rootfsMgr     *rootfs.Manager
	snapshotter   *snapshotter.Snapshotter
	store         *metadata.Store
	configBuilder *metadata.ConfigBuilder
	tracker       *metrics.Tracker

	buildContext string   // path to the build context directory
	workDir      string   // current WORKDIR (accumulated across instructions)
	env          []string // accumulated ENV variables
	shell        []string // current SHELL setting
	lowerDirs    []string // accumulated layer directories for overlay stacking
}

// Config holds the configuration for creating a new Executor.
type Config struct {
	BuildContext string // path to the build context (files for COPY)
	WorkDir      string // base working directory for temp files
	Store        *metadata.Store
	Tracker      *metrics.Tracker
}

// New creates a new build executor.
func New(cfg Config) (*Executor, error) {
	// Create sub-directories for rootfs and blobs
	rootfsDir := filepath.Join(cfg.WorkDir, "rootfs")
	blobDir := filepath.Join(cfg.WorkDir, "blobs")

	rootfsMgr, err := rootfs.NewManager(rootfsDir)
	if err != nil {
		return nil, fmt.Errorf("executor: create rootfs manager: %w", err)
	}

	snap, err := snapshotter.New(blobDir, cfg.Tracker)
	if err != nil {
		return nil, fmt.Errorf("executor: create snapshotter: %w", err)
	}

	return &Executor{
		rootfsMgr:     rootfsMgr,
		snapshotter:   snap,
		store:         cfg.Store,
		configBuilder: metadata.NewConfigBuilder(),
		tracker:       cfg.Tracker,
		buildContext:  cfg.BuildContext,
		workDir:       "/",
		env:           []string{},
		shell:         []string{"/bin/sh", "-c"},
		lowerDirs:     make([]string, 0, 8),
	}, nil
}

// ExecuteStage runs all instructions in a single build stage.
func (e *Executor) ExecuteStage(ctx context.Context, stage *types.BuildStage) error {
	for _, inst := range stage.Instructions {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := e.executeInstruction(ctx, &inst); err != nil {
			return fmt.Errorf("stage %d, line %d (%s): %w",
				stage.Index, inst.Line, inst.Type, err)
		}
	}
	return nil
}

// executeInstruction executes a single Dockerfile instruction.
func (e *Executor) executeInstruction(ctx context.Context, inst *types.Instruction) error {
	// Phase: Cache lookup
	if e.tracker != nil {
		e.tracker.StartPhase("Cache Lookup")
	}
	parentChainID := e.computeChainID()
	instrHash := metadata.InstructionHash(inst.Raw)
	cached := e.store.CacheLookup(parentChainID, instrHash)
	if e.tracker != nil {
		e.tracker.EndPhase("Cache Lookup")
	}

	if cached != nil {
		// Cache hit — skip execution
		e.store.AddLayer(cached)
		e.configBuilder.AddDiffID(string(cached.DiffID))
		e.configBuilder.AddHistory(inst.Raw, false)
		return nil
	}

	// Dispatch to the appropriate instruction handler
	handler, ok := instructionHandlers[inst.Type]
	if !ok {
		return fmt.Errorf("unsupported instruction: %s", inst.Type)
	}

	return handler(ctx, e, inst)
}

// computeChainID returns the chain ID for the current layer stack.
func (e *Executor) computeChainID() string {
	layers := e.store.GetLayers()
	if len(layers) == 0 {
		return ""
	}
	diffIDs := make([]types.Digest, len(layers))
	for i, l := range layers {
		diffIDs[i] = l.DiffID
	}
	return metadata.ComputeChainID(diffIDs)
}

// executeFilesystemInstruction handles instructions that modify the filesystem
// (RUN, COPY, ADD). This is the main hot path:
//  1. Prepare rootfs (mount overlay)
//  2. Execute the action (run command / copy files)
//  3. Snapshot the changes
//  4. Store metadata
func (e *Executor) executeFilesystemInstruction(ctx context.Context, inst *types.Instruction, action func(mergedDir string) error) error {
	// Phase: Prepare rootfs
	if e.tracker != nil {
		e.tracker.StartPhase("RootFS Prepare")
	}
	mergedDir, upperDir, err := e.rootfsMgr.Prepare(ctx, e.lowerDirs)
	if err != nil {
		return fmt.Errorf("prepare rootfs: %w", err)
	}
	if e.tracker != nil {
		e.tracker.EndPhase("RootFS Prepare")
	}
	defer e.rootfsMgr.Cleanup(ctx, mergedDir)

	// Phase: Execute instruction
	if e.tracker != nil {
		e.tracker.StartPhase("Instruction Execute")
	}
	if err := action(mergedDir); err != nil {
		return fmt.Errorf("execute action: %w", err)
	}
	if e.tracker != nil {
		e.tracker.EndPhase("Instruction Execute")
	}

	// Phase: Commit
	if err := e.rootfsMgr.Commit(ctx, mergedDir); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Phase: Snapshot
	if e.tracker != nil {
		e.tracker.StartPhase("Layer Snapshot")
	}
	layer, err := e.snapshotter.Snapshot(ctx, upperDir, inst.Raw)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if e.tracker != nil {
		e.tracker.EndPhase("Layer Snapshot")
	}

	// Phase: Store metadata
	if e.tracker != nil {
		e.tracker.StartPhase("Metadata Store")
	}
	e.store.AddLayer(layer)
	e.configBuilder.AddDiffID(string(layer.DiffID))
	e.configBuilder.AddHistory(inst.Raw, false)

	// Cache the result
	parentChainID := e.computeChainID()
	instrHash := metadata.InstructionHash(inst.Raw)
	e.store.CacheStore(parentChainID, instrHash, layer)

	// Add upper dir as a lower dir for the next instruction
	e.lowerDirs = append(e.lowerDirs, upperDir)
	if e.tracker != nil {
		e.tracker.EndPhase("Metadata Store")
	}

	return nil
}

// executeMetadataInstruction handles instructions that only modify config.
func (e *Executor) executeMetadataInstruction(inst *types.Instruction, apply func()) {
	apply()
	e.configBuilder.AddHistory(inst.Raw, true) // empty_layer = true
}

// BuildConfig returns the finalized image configuration.
func (e *Executor) BuildConfig() *types.ImageConfig {
	return e.configBuilder.Build()
}

// Cleanup releases all executor resources.
func (e *Executor) Cleanup() error {
	return e.rootfsMgr.Close()
}

// resolveEnv substitutes $VAR and ${VAR} in a string using the current env.
func (e *Executor) resolveEnv(s string) string {
	envMap := make(map[string]string, len(e.env))
	for _, kv := range e.env {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			envMap[kv[:idx]] = kv[idx+1:]
		}
	}

	return os.Expand(s, func(key string) string {
		if val, ok := envMap[key]; ok {
			return val
		}
		return ""
	})
}
