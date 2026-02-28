package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/pool"
)

// instructionHandler is a function that handles a specific instruction type.
type instructionHandler func(ctx context.Context, e *Executor, inst *types.Instruction) error

// instructionHandlers is the jump table mapping instruction type → handler.
// Using a map gives O(1) dispatch and avoids branch-prediction misses.
var instructionHandlers = map[types.InstructionType]instructionHandler{
	types.InstructionFrom:        handleFrom,
	types.InstructionRun:         handleRun,
	types.InstructionCopy:        handleCopy,
	types.InstructionAdd:         handleAdd,
	types.InstructionCmd:         handleCmd,
	types.InstructionEntrypoint:  handleEntrypoint,
	types.InstructionEnv:         handleEnv,
	types.InstructionExpose:      handleExpose,
	types.InstructionVolume:      handleVolume,
	types.InstructionWorkdir:     handleWorkdir,
	types.InstructionUser:        handleUser,
	types.InstructionLabel:       handleLabel,
	types.InstructionShell:       handleShell,
	types.InstructionStopsignal:  handleStopSignal,
	types.InstructionArg:         handleArg,
	types.InstructionHealthcheck: handleHealthcheck,
	types.InstructionMaintainer:  handleMaintainer,
}

// ---------------------------------------------------------------------------
// FROM — starts a new build stage
// ---------------------------------------------------------------------------

func handleFrom(_ context.Context, e *Executor, inst *types.Instruction) error {
	// FROM resets the layer stack for a new stage.
	// In a real builder, this would pull the base image and extract its layers.
	// For our implementation, we start with an empty rootfs.
	e.lowerDirs = e.lowerDirs[:0]
	e.workDir = "/"
	e.configBuilder.AddHistory(inst.Raw, true)
	return nil
}

// ---------------------------------------------------------------------------
// RUN — executes a shell command in the rootfs
// ---------------------------------------------------------------------------

func handleRun(ctx context.Context, e *Executor, inst *types.Instruction) error {
	return e.executeFilesystemInstruction(ctx, inst, func(mergedDir string) error {
		// Construct the command
		var cmdArgs []string
		if len(inst.Args) == 1 {
			// Shell form: RUN <command>
			cmdArgs = append(e.shell, inst.Args[0])
		} else {
			// Exec form: RUN ["executable", "param1", "param2"]
			cmdArgs = inst.Args
		}

		// Ensure the working directory exists in the merged rootfs
		workPath := filepath.Join(mergedDir, filepath.FromSlash(e.workDir))
		os.MkdirAll(workPath, 0755)

		if runtime.GOOS == "linux" {
			return runInChroot(ctx, mergedDir, workPath, cmdArgs, e.env)
		}
		return runSimulated(ctx, mergedDir, workPath, cmdArgs, e.env)
	})
}

// runInChroot executes a command inside a chroot on Linux.
func runInChroot(ctx context.Context, rootDir, workDir string, cmdArgs, env []string) error {
	if len(cmdArgs) == 0 {
		return fmt.Errorf("empty command")
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Note: Real chroot requires root privileges. In a production builder,
	// we'd use syscall.Chroot or Linux namespaces. For now we run the
	// command with the merged dir as a working base.
	cmd.Dir = rootDir

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("RUN failed: %w", err)
	}
	return nil
}

// runSimulated simulates RUN on non-Linux by writing a marker file.
// This allows the builder to work on Windows/macOS for development.
func runSimulated(_ context.Context, rootDir, workDir string, cmdArgs, env []string) error {
	// Create a marker file indicating what command would have run
	markerPath := filepath.Join(rootDir, ".imagin-run-marker")
	content := fmt.Sprintf("# Simulated RUN (non-Linux)\n# Command: %s\n# WorkDir: %s\n# Env: %v\n",
		strings.Join(cmdArgs, " "), workDir, env)
	return os.WriteFile(markerPath, []byte(content), 0644)
}

// ---------------------------------------------------------------------------
// COPY — copies files from build context into the rootfs
// ---------------------------------------------------------------------------

func handleCopy(ctx context.Context, e *Executor, inst *types.Instruction) error {
	return e.executeFilesystemInstruction(ctx, inst, func(mergedDir string) error {
		if len(inst.Args) < 2 {
			return fmt.Errorf("COPY requires source and destination")
		}

		// Last arg is destination, all preceding are sources
		dstRel := inst.Args[len(inst.Args)-1]
		sources := inst.Args[:len(inst.Args)-1]

		// Resolve destination relative to WORKDIR
		dst := filepath.Join(mergedDir, filepath.FromSlash(e.workDir), filepath.FromSlash(dstRel))

		// Check for --from flag (multi-stage copy)
		if fromStage, ok := inst.Flags["from"]; ok {
			// In a full builder, this would copy from another stage's rootfs.
			// For now, note it as a marker.
			_ = fromStage
			return fmt.Errorf("COPY --from=%s not yet fully implemented", fromStage)
		}

		for _, src := range sources {
			srcResolved := e.resolveEnv(src)
			srcPath := filepath.Join(e.buildContext, filepath.FromSlash(srcResolved))

			info, err := os.Stat(srcPath)
			if err != nil {
				return fmt.Errorf("COPY source %q not found: %w", srcResolved, err)
			}

			if info.IsDir() {
				if err := copyDirectory(srcPath, dst); err != nil {
					return fmt.Errorf("COPY dir %s → %s: %w", srcResolved, dstRel, err)
				}
			} else {
				// Ensure destination directory exists
				os.MkdirAll(filepath.Dir(dst), 0755)
				if err := copyFilePooled(srcPath, dst); err != nil {
					return fmt.Errorf("COPY file %s → %s: %w", srcResolved, dstRel, err)
				}
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// ADD — like COPY but with auto-extraction (simplified)
// ---------------------------------------------------------------------------

func handleAdd(ctx context.Context, e *Executor, inst *types.Instruction) error {
	// ADD is similar to COPY; for simplicity, we delegate to the same logic.
	// A full implementation would handle URL downloads and tar auto-extraction.
	return handleCopy(ctx, e, inst)
}

// ---------------------------------------------------------------------------
// Metadata-only instructions
// ---------------------------------------------------------------------------

func handleCmd(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		e.configBuilder.SetCmd(inst.Args)
	})
	return nil
}

func handleEntrypoint(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		e.configBuilder.SetEntrypoint(inst.Args)
	})
	return nil
}

func handleEnv(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		for _, arg := range inst.Args {
			e.configBuilder.AddEnv(arg)
			e.env = append(e.env, arg)
		}
	})
	return nil
}

func handleExpose(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		for _, port := range inst.Args {
			if !strings.Contains(port, "/") {
				port = port + "/tcp" // default to TCP
			}
			e.configBuilder.AddExposedPort(port)
		}
	})
	return nil
}

func handleVolume(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		for _, vol := range inst.Args {
			e.configBuilder.AddVolume(vol)
		}
	})
	return nil
}

func handleWorkdir(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		if len(inst.Args) > 0 {
			dir := e.resolveEnv(inst.Args[0])
			// Container paths are always Linux-style forward slashes,
			// regardless of the host OS.
			if len(dir) > 0 && dir[0] == '/' {
				e.workDir = dir
			} else {
				e.workDir = e.workDir + "/" + dir
			}
			// Normalize any double slashes
			for strings.Contains(e.workDir, "//") {
				e.workDir = strings.ReplaceAll(e.workDir, "//", "/")
			}
			e.configBuilder.SetWorkdir(e.workDir)
		}
	})
	return nil
}

func handleUser(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		if len(inst.Args) > 0 {
			e.configBuilder.SetUser(inst.Args[0])
		}
	})
	return nil
}

func handleLabel(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		for _, arg := range inst.Args {
			if idx := strings.IndexByte(arg, '='); idx >= 0 {
				e.configBuilder.AddLabel(arg[:idx], arg[idx+1:])
			}
		}
	})
	return nil
}

func handleShell(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		if len(inst.Args) > 0 {
			e.shell = inst.Args
			e.configBuilder.SetShell(inst.Args)
		}
	})
	return nil
}

func handleStopSignal(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		if len(inst.Args) > 0 {
			e.configBuilder.SetStopSignal(inst.Args[0])
		}
	})
	return nil
}

func handleArg(_ context.Context, e *Executor, inst *types.Instruction) error {
	// ARG sets build-time variables. In a full implementation, these would
	// be stored and used for variable substitution in subsequent instructions.
	e.executeMetadataInstruction(inst, func() {
		// Store as env for substitution purposes
		for _, arg := range inst.Args {
			e.env = append(e.env, arg)
		}
	})
	return nil
}

func handleHealthcheck(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		// HEALTHCHECK configuration is stored in the config
		// Simplified: just record in history
	})
	return nil
}

func handleMaintainer(_ context.Context, e *Executor, inst *types.Instruction) error {
	e.executeMetadataInstruction(inst, func() {
		if len(inst.Args) > 0 {
			e.configBuilder.AddLabel("maintainer", strings.Join(inst.Args, " "))
		}
	})
	return nil
}

// ---------------------------------------------------------------------------
// File copy helpers
// ---------------------------------------------------------------------------

func copyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFilePooled(path, target)
	})
}

func copyFilePooled(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()

	os.MkdirAll(filepath.Dir(dst), 0755)
	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer df.Close()

	buf := pool.GetLargeBuffer()
	defer pool.PutLargeBuffer(buf)

	_, err = io.CopyBuffer(df, sf, *buf)
	return err
}
