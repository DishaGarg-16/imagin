package snapshotter

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/imagin/imagin/pkg/pool"
)

// WriteTarLayer writes a tar.gz layer to dest from the given changes in rootDir.
// Uses the StreamingPipeline for simultaneous hash + compress in a single pass.
//
// Returns: (diffID, distributionDigest, compressedSize, uncompressedSize, error)
//
// Latency optimisations:
//   - Streaming pipeline: no intermediate buffering of the entire layer
//   - Pooled buffers:     reused across layers via sync.Pool
//   - Pooled gzip writer: avoids expensive gzip.NewWriter allocation
//   - Single-pass hashing: both compressed and uncompressed digests in one pass
func WriteTarLayer(dest io.Writer, rootDir string, changes []Change) (
	diffID string,
	distDigest string,
	compressedSize int64,
	uncompressedSize int64,
	err error,
) {
	// Set up the streaming pipeline: tar → sha256 → gzip → sha256 → dest
	pipeline := pool.NewStreamingPipeline(dest)

	// Create tar writer on top of the pipeline
	tw := tar.NewWriter(pipeline.Writer)

	// Get a pooled buffer for file copying
	buf := pool.GetLargeBuffer()
	defer pool.PutLargeBuffer(buf)

	// Write each change as a tar entry
	for _, change := range changes {
		if change.Kind == ChangeDelete {
			// Write a whiteout entry: .wh.<filename>
			if err = writeWhiteout(tw, change.Path); err != nil {
				return "", "", 0, 0, fmt.Errorf("write whiteout for %s: %w", change.Path, err)
			}
			continue
		}

		// Add or Modify: write the file/directory
		fullPath := filepath.Join(rootDir, filepath.FromSlash(change.Path))

		if change.Info == nil {
			// Shouldn't happen for Add/Modify, but be safe
			info, statErr := os.Lstat(fullPath)
			if statErr != nil {
				continue
			}
			change.Info = info
		}

		if change.Info.IsDir() {
			if err = writeDirEntry(tw, change.Path, change.Info); err != nil {
				return "", "", 0, 0, fmt.Errorf("write dir %s: %w", change.Path, err)
			}
		} else if change.Info.Mode().IsRegular() {
			n, writeErr := writeFileEntry(tw, change.Path, fullPath, change.Info, *buf)
			if writeErr != nil {
				return "", "", 0, 0, fmt.Errorf("write file %s: %w", change.Path, writeErr)
			}
			uncompressedSize += n
		} else if change.Info.Mode()&os.ModeSymlink != 0 {
			if err = writeSymlinkEntry(tw, change.Path, fullPath, change.Info); err != nil {
				return "", "", 0, 0, fmt.Errorf("write symlink %s: %w", change.Path, err)
			}
		}
	}

	// Close tar writer (flushes padding)
	if err = tw.Close(); err != nil {
		return "", "", 0, 0, fmt.Errorf("close tar writer: %w", err)
	}

	// Close pipeline (flushes gzip, computes final digests)
	diffID, distDigest, compressedSize, err = pipeline.Close()
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("close pipeline: %w", err)
	}

	return diffID, distDigest, compressedSize, uncompressedSize, nil
}

// writeDirEntry writes a directory entry to the tar archive.
func writeDirEntry(tw *tar.Writer, relPath string, info os.FileInfo) error {
	header := &tar.Header{
		Typeflag: tar.TypeDir,
		Name:     relPath + "/",
		Mode:     int64(info.Mode()),
		ModTime:  info.ModTime(),
	}
	return tw.WriteHeader(header)
}

// writeFileEntry writes a regular file entry to the tar archive.
// Returns the number of bytes written (uncompressed).
func writeFileEntry(tw *tar.Writer, relPath, fullPath string, info os.FileInfo, buf []byte) (int64, error) {
	header := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     relPath,
		Size:     info.Size(),
		Mode:     int64(info.Mode()),
		ModTime:  info.ModTime(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return 0, err
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n, err := io.CopyBuffer(tw, f, buf)
	return n, err
}

// writeSymlinkEntry writes a symlink entry to the tar archive.
func writeSymlinkEntry(tw *tar.Writer, relPath, fullPath string, info os.FileInfo) error {
	target, err := os.Readlink(fullPath)
	if err != nil {
		return err
	}

	header := &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     relPath,
		Linkname: target,
		Mode:     int64(info.Mode()),
		ModTime:  info.ModTime(),
	}
	return tw.WriteHeader(header)
}

// writeWhiteout writes an OCI whiteout marker for a deleted file.
// The OCI spec represents deletions as zero-length files named .wh.<filename>.
func writeWhiteout(tw *tar.Writer, relPath string) error {
	dir := filepath.Dir(relPath)
	base := filepath.Base(relPath)
	whiteoutName := filepath.ToSlash(filepath.Join(dir, ".wh."+base))

	header := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     whiteoutName,
		Size:     0,
		Mode:     0644,
	}
	return tw.WriteHeader(header)
}
