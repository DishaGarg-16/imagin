package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/metadata"
)

func setupTestExecutor(t *testing.T) (*Executor, string, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "executor-test-*")
	if err != nil {
		t.Fatal(err)
	}

	contextDir := filepath.Join(tmpDir, "context")
	os.MkdirAll(contextDir, 0755)
	os.WriteFile(filepath.Join(contextDir, "app.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(contextDir, "README.md"), []byte("# Hello"), 0644)

	workDir := filepath.Join(tmpDir, "work")
	os.MkdirAll(workDir, 0755)

	store := metadata.NewStore()

	exec, err := New(Config{
		BuildContext: contextDir,
		WorkDir:      workDir,
		Store:        store,
		Tracker:      nil,
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	cleanup := func() {
		exec.Cleanup()
		os.RemoveAll(tmpDir)
	}

	return exec, contextDir, cleanup
}

func TestExecuteMetadataInstructions(t *testing.T) {
	exec, _, cleanup := setupTestExecutor(t)
	defer cleanup()

	ctx := context.Background()

	// Simulate a FROM instruction
	err := exec.executeInstruction(ctx, &types.Instruction{
		Type: types.InstructionFrom,
		Args: []string{"ubuntu:22.04"},
		Raw:  "FROM ubuntu:22.04",
		Line: 1,
	})
	if err != nil {
		t.Fatalf("FROM failed: %v", err)
	}

	// ENV instruction
	err = exec.executeInstruction(ctx, &types.Instruction{
		Type: types.InstructionEnv,
		Args: []string{"APP_HOME=/app"},
		Raw:  "ENV APP_HOME=/app",
		Line: 2,
	})
	if err != nil {
		t.Fatalf("ENV failed: %v", err)
	}

	// WORKDIR instruction
	err = exec.executeInstruction(ctx, &types.Instruction{
		Type: types.InstructionWorkdir,
		Args: []string{"/app"},
		Raw:  "WORKDIR /app",
		Line: 3,
	})
	if err != nil {
		t.Fatalf("WORKDIR failed: %v", err)
	}

	// CMD instruction
	err = exec.executeInstruction(ctx, &types.Instruction{
		Type: types.InstructionCmd,
		Args: []string{"./start"},
		Raw:  "CMD [\"./start\"]",
		Line: 4,
	})
	if err != nil {
		t.Fatalf("CMD failed: %v", err)
	}

	// Verify the config
	cfg := exec.BuildConfig()
	if cfg.Config.WorkingDir != "/app" {
		t.Errorf("expected WorkingDir=/app, got %s", cfg.Config.WorkingDir)
	}
	if len(cfg.Config.Cmd) != 1 || cfg.Config.Cmd[0] != "./start" {
		t.Errorf("unexpected Cmd: %v", cfg.Config.Cmd)
	}

	// Check that APP_HOME env was set
	foundEnv := false
	for _, env := range cfg.Config.Env {
		if env == "APP_HOME=/app" {
			foundEnv = true
			break
		}
	}
	if !foundEnv {
		t.Error("APP_HOME not found in env")
	}
}

func TestExecuteCopyInstruction(t *testing.T) {
	exec, contextDir, cleanup := setupTestExecutor(t)
	defer cleanup()

	ctx := context.Background()

	// FROM first
	exec.executeInstruction(ctx, &types.Instruction{
		Type: types.InstructionFrom,
		Args: []string{"scratch"},
		Raw:  "FROM scratch",
		Line: 1,
	})

	// COPY . /app
	err := exec.executeInstruction(ctx, &types.Instruction{
		Type:  types.InstructionCopy,
		Args:  []string{".", "/app"},
		Flags: map[string]string{},
		Raw:   "COPY . /app",
		Line:  2,
	})
	if err != nil {
		t.Fatalf("COPY failed: %v", err)
	}

	// Verify layers were created
	layers := exec.store.GetLayers()
	if len(layers) < 1 {
		t.Fatalf("expected at least 1 layer, got %d", len(layers))
	}

	// Verify the layer blob exists
	lastLayer := layers[len(layers)-1]
	if lastLayer.BlobPath == "" {
		t.Error("layer blob path is empty")
	}
	if lastLayer.Size <= 0 {
		t.Error("layer size should be > 0")
	}

	_ = contextDir
}

func TestBuildContextIgnore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "context-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create files
	os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package main"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg", "index.js"), []byte(""), 0644)
	os.WriteFile(filepath.Join(tmpDir, ".git"), []byte(""), 0644)

	// Create .dockerignore
	os.WriteFile(filepath.Join(tmpDir, ".dockerignore"), []byte("node_modules\n.git\n"), 0644)

	bc, err := NewBuildContext(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if bc.IsIgnored("app.go") {
		t.Error("app.go should not be ignored")
	}
	if !bc.IsIgnored("node_modules") {
		t.Error("node_modules should be ignored")
	}
	if !bc.IsIgnored(".git") {
		t.Error(".git should be ignored")
	}

	// Walk files and verify ignored files are skipped
	var walked []string
	bc.WalkFiles(func(rel string, info os.FileInfo) error {
		walked = append(walked, rel)
		return nil
	})

	for _, f := range walked {
		if f == "node_modules" || f == ".git" {
			t.Errorf("walked file should have been ignored: %s", f)
		}
	}
}

func TestResolveEnv(t *testing.T) {
	exec, _, cleanup := setupTestExecutor(t)
	defer cleanup()

	exec.env = []string{"HOME=/app", "VERSION=1.0"}

	result := exec.resolveEnv("$HOME/bin/$VERSION")
	if result != "/app/bin/1.0" {
		t.Errorf("expected /app/bin/1.0, got %q", result)
	}

	result = exec.resolveEnv("${HOME}/config")
	if result != "/app/config" {
		t.Errorf("expected /app/config, got %q", result)
	}
}

func TestExecuteStage(t *testing.T) {
	exec, _, cleanup := setupTestExecutor(t)
	defer cleanup()

	stage := &types.BuildStage{
		Name:  "",
		Index: 0,
		Instructions: []types.Instruction{
			{Type: types.InstructionFrom, Args: []string{"scratch"}, Raw: "FROM scratch", Line: 1},
			{Type: types.InstructionEnv, Args: []string{"FOO=bar"}, Raw: "ENV FOO=bar", Line: 2},
			{Type: types.InstructionWorkdir, Args: []string{"/app"}, Raw: "WORKDIR /app", Line: 3},
			{Type: types.InstructionCmd, Args: []string{"/bin/sh"}, Raw: "CMD [\"/bin/sh\"]", Line: 4},
		},
	}

	err := exec.ExecuteStage(context.Background(), stage)
	if err != nil {
		t.Fatalf("ExecuteStage failed: %v", err)
	}

	cfg := exec.BuildConfig()
	if cfg.Config.WorkingDir != "/app" {
		t.Errorf("expected WorkingDir=/app, got %s", cfg.Config.WorkingDir)
	}
}
