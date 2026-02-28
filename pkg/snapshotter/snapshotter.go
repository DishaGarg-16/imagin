// Package snapshotter captures filesystem changes into compressed tar layers.
// This is the most latency-sensitive component — it uses streaming pipelines,
// pooled buffers, and parallel hash+compress to minimise p95/p99 tail latency.
package snapshotter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/metrics"
)

// Snapshotter creates layers from filesystem diffs.
type Snapshotter struct {
	blobDir string           // directory to store layer blobs
	tracker *metrics.Tracker // optional metrics tracker
}

// New creates a Snapshotter that stores layer blobs in blobDir.
func New(blobDir string, tracker *metrics.Tracker) (*Snapshotter, error) {
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return nil, fmt.Errorf("snapshotter: create blob dir: %w", err)
	}
	return &Snapshotter{
		blobDir: blobDir,
		tracker: tracker,
	}, nil
}

// Snapshot creates a layer from the filesystem changes in upperDir.
// Steps:
//  1. Walk the upperDir to discover changed/added/deleted files
//  2. Write a tar archive of the changes through a streaming pipeline
//     (tar → SHA256(uncompressed) → gzip → SHA256(compressed) → file)
//  3. Return Layer metadata with both digests and sizes
func (s *Snapshotter) Snapshot(ctx context.Context, upperDir string, createdBy string) (*types.Layer, error) {
	// Phase 1: Compute the diff (list of changes)
	if s.tracker != nil {
		s.tracker.StartSubPhase("Layer Snapshot", "Diff")
	}
	changes, err := DiffDir(upperDir)
	if err != nil {
		return nil, fmt.Errorf("snapshotter: diff failed: %w", err)
	}
	if s.tracker != nil {
		s.tracker.EndSubPhase("Layer Snapshot", "Diff")
	}

	// Create a temp file for the layer blob
	blobFile, err := os.CreateTemp(s.blobDir, "layer-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("snapshotter: create blob file: %w", err)
	}
	blobPath := blobFile.Name()

	// Phase 2: Write the tar archive through the streaming pipeline.
	// The pipeline simultaneously compresses and hashes in a single pass.
	if s.tracker != nil {
		s.tracker.StartSubPhase("Layer Snapshot", "Tar+Compress")
	}
	diffID, distDigest, compressedSize, uncompSize, err := WriteTarLayer(blobFile, upperDir, changes)
	blobFile.Close()
	if err != nil {
		os.Remove(blobPath)
		return nil, fmt.Errorf("snapshotter: write tar: %w", err)
	}
	if s.tracker != nil {
		s.tracker.EndSubPhase("Layer Snapshot", "Tar+Compress")
	}

	// Rename blob to its digest for content-addressable storage
	finalName := digestToFilename(distDigest)
	finalPath := filepath.Join(s.blobDir, finalName)
	if err := os.Rename(blobPath, finalPath); err != nil {
		// If rename fails (cross-device), keep original path
		finalPath = blobPath
	}

	layer := &types.Layer{
		DiffID:     types.Digest(diffID),
		Digest:     types.Digest(distDigest),
		Size:       compressedSize,
		UncompSize: uncompSize,
		MediaType:  "application/vnd.oci.image.layer.v1.tar+gzip",
		CreatedBy:  createdBy,
		CreatedAt:  time.Now().UTC(),
		BlobPath:   finalPath,
	}

	return layer, nil
}

// SnapshotEmpty creates an empty layer (for metadata-only instructions).
func (s *Snapshotter) SnapshotEmpty(createdBy string) *types.Layer {
	return &types.Layer{
		DiffID:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Size:      0,
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(),
	}
}

// digestToFilename converts "sha256:abc123..." to "sha256-abc123..."
func digestToFilename(d string) string {
	result := make([]byte, len(d))
	for i := 0; i < len(d); i++ {
		if d[i] == ':' {
			result[i] = '-'
		} else {
			result[i] = d[i]
		}
	}
	return string(result)
}
