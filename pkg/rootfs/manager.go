// Package rootfs manages the layered root filesystem for build steps.
// It provides two backends: OverlayFS (Linux only, fast) and a copy-based
// fallback (portable, works on Windows/macOS).
package rootfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Manager implements types.RootFSManager using directory-based layer stacking.
// On non-Linux systems (or without privileges), it uses a copy-based fallback.
type Manager struct {
	baseDir    string // root directory for all layer data
	workDir    string // temp work directory
	mounts     map[string]*MountInfo
	mu         sync.Mutex
	useOverlay bool
}

// MountInfo tracks a single active mount point.
type MountInfo struct {
	MergedDir string   // the combined view
	UpperDir  string   // writable layer (changes go here)
	WorkDir   string   // overlay workdir (required by overlayfs)
	LowerDirs []string // read-only layers below
}

// NewManager creates a new RootFS manager rooted at baseDir.
func NewManager(baseDir string) (*Manager, error) {
	workDir := filepath.Join(baseDir, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("rootfs: failed to create work dir: %w", err)
	}

	m := &Manager{
		baseDir:    baseDir,
		workDir:    workDir,
		mounts:     make(map[string]*MountInfo),
		useOverlay: isOverlayAvailable(),
	}
	return m, nil
}

// Prepare sets up a writable layer on top of the given lower directories.
// Returns the merged (combined view) dir and the upper (writable) dir.
func (m *Manager) Prepare(ctx context.Context, lowerDirs []string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create unique directories for this mount
	id := fmt.Sprintf("layer-%d", len(m.mounts))
	upperDir := filepath.Join(m.workDir, id, "upper")
	mergedDir := filepath.Join(m.workDir, id, "merged")
	oWorkDir := filepath.Join(m.workDir, id, "owork")

	for _, d := range []string{upperDir, mergedDir, oWorkDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return "", "", fmt.Errorf("rootfs: failed to create %s: %w", d, err)
		}
	}

	info := &MountInfo{
		MergedDir: mergedDir,
		UpperDir:  upperDir,
		WorkDir:   oWorkDir,
		LowerDirs: lowerDirs,
	}

	// Use overlay if available, otherwise fallback to copy
	if m.useOverlay {
		if err := mountOverlay(info); err != nil {
			// Fall back to copy if overlay fails
			if err := prepareFallback(info); err != nil {
				return "", "", fmt.Errorf("rootfs: fallback failed: %w", err)
			}
		}
	} else {
		if err := prepareFallback(info); err != nil {
			return "", "", fmt.Errorf("rootfs: prepare failed: %w", err)
		}
	}

	m.mounts[mergedDir] = info
	return mergedDir, upperDir, nil
}

// Commit finalises the current layer. With overlay, this is a no-op since
// the upper dir already contains the diff. With fallback, we compute the diff.
func (m *Manager) Commit(ctx context.Context, mergedDir string) error {
	m.mu.Lock()
	_, ok := m.mounts[mergedDir]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("rootfs: no mount found for %s", mergedDir)
	}

	// The upper dir already contains the diff — nothing to do for overlay.
	// For fallback, the diff is computed by the snapshotter.
	return nil
}

// Cleanup tears down mounts and temp dirs for the given merged path.
func (m *Manager) Cleanup(ctx context.Context, mergedDir string) error {
	m.mu.Lock()
	info, ok := m.mounts[mergedDir]
	if ok {
		delete(m.mounts, mergedDir)
	}
	m.mu.Unlock()

	if !ok {
		return nil
	}

	if m.useOverlay {
		unmountOverlay(info)
	}

	// Clean up the layer directories
	layerDir := filepath.Dir(info.UpperDir)
	return os.RemoveAll(layerDir)
}

// Close releases all resources held by the manager.
func (m *Manager) Close() error {
	m.mu.Lock()
	mounts := make(map[string]*MountInfo, len(m.mounts))
	for k, v := range m.mounts {
		mounts[k] = v
	}
	m.mu.Unlock()

	for mergedDir := range mounts {
		m.Cleanup(context.Background(), mergedDir)
	}
	return nil
}

// GetUpperDir returns the upper (writable) directory for a mount.
func (m *Manager) GetUpperDir(mergedDir string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, ok := m.mounts[mergedDir]
	if !ok {
		return "", fmt.Errorf("rootfs: no mount found for %s", mergedDir)
	}
	return info.UpperDir, nil
}
