package rootfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/imagin/imagin/pkg/pool"
)

// prepareFallback implements a copy-based rootfs stacking for non-Linux
// environments. It copies all lower layer contents into the merged dir,
// then the upper dir records changes on top.
//
// This is slower than OverlayFS (full copy instead of CoW) but works
// everywhere. Good for development, testing, and CI on macOS/Windows.
func prepareFallback(info *MountInfo) error {
	// Copy each lower dir into merged, in order (first = base).
	for _, lowerDir := range info.LowerDirs {
		if err := copyDir(lowerDir, info.MergedDir); err != nil {
			return fmt.Errorf("fallback: copying lower dir %s: %w", lowerDir, err)
		}
	}
	return nil
}

// copyDir recursively copies src into dst. Uses pooled buffers to reduce
// allocation pressure.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, rel)

		if fi.IsDir() {
			return os.MkdirAll(targetPath, fi.Mode())
		}

		return copyFile(path, targetPath, fi.Mode())
	})
}

// copyFile copies a single file using a pooled buffer.
func copyFile(src, dst string, mode os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Ensure parent dir exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	buf := pool.GetLargeBuffer()
	defer pool.PutLargeBuffer(buf)

	_, err = io.CopyBuffer(dstFile, srcFile, *buf)
	return err
}
