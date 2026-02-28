package rootfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerPrepareCleanup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rootfs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	// Create a fake lower dir with a file
	lowerDir := filepath.Join(tmpDir, "lower0")
	os.MkdirAll(lowerDir, 0755)
	os.WriteFile(filepath.Join(lowerDir, "hello.txt"), []byte("hello"), 0644)

	ctx := context.Background()
	mergedDir, upperDir, err := mgr.Prepare(ctx, []string{lowerDir})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Verify merged dir contains the file from lower (fallback mode)
	if !mgr.useOverlay {
		data, err := os.ReadFile(filepath.Join(mergedDir, "hello.txt"))
		if err != nil {
			t.Fatalf("merged dir missing hello.txt: %v", err)
		}
		if string(data) != "hello" {
			t.Errorf("unexpected content: %q", string(data))
		}
	}

	// Write a new file to the upper dir (simulating instruction execution)
	os.WriteFile(filepath.Join(upperDir, "new.txt"), []byte("new file"), 0644)

	// Commit
	if err := mgr.Commit(ctx, mergedDir); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify upper dir has the new file
	if _, err := os.Stat(filepath.Join(upperDir, "new.txt")); err != nil {
		t.Error("upper dir missing new.txt after commit")
	}

	// Cleanup
	if err := mgr.Cleanup(ctx, mergedDir); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Directories should be gone
	if _, err := os.Stat(mergedDir); !os.IsNotExist(err) {
		t.Error("merged dir should be removed after cleanup")
	}
}

func TestManagerMultipleLayers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rootfs-multi-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// Layer 0: base with file A
	lower0 := filepath.Join(tmpDir, "lower0")
	os.MkdirAll(lower0, 0755)
	os.WriteFile(filepath.Join(lower0, "a.txt"), []byte("A"), 0644)

	merged0, upper0, err := mgr.Prepare(ctx, []string{lower0})
	if err != nil {
		t.Fatalf("Prepare layer 0: %v", err)
	}
	// Add file B in layer 0
	os.WriteFile(filepath.Join(upper0, "b.txt"), []byte("B"), 0644)
	mgr.Commit(ctx, merged0)

	// Layer 1: stack on top of lower0 + upper0
	merged1, upper1, err := mgr.Prepare(ctx, []string{lower0, upper0})
	if err != nil {
		t.Fatalf("Prepare layer 1: %v", err)
	}
	// Add file C in layer 1
	os.WriteFile(filepath.Join(upper1, "c.txt"), []byte("C"), 0644)
	mgr.Commit(ctx, merged1)

	// In fallback mode, merged1 should contain A, B, and C
	if !mgr.useOverlay {
		for _, fname := range []string{"a.txt", "b.txt"} {
			if _, err := os.Stat(filepath.Join(merged1, fname)); err != nil {
				t.Errorf("merged dir missing %s: %v", fname, err)
			}
		}
	}

	// Upper dir only has C
	if _, err := os.Stat(filepath.Join(upper1, "c.txt")); err != nil {
		t.Error("upper dir missing c.txt")
	}

	mgr.Cleanup(ctx, merged0)
	mgr.Cleanup(ctx, merged1)
}
