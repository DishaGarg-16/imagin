// Package exporter writes OCI Image Layout directories and tar archives.
package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/imagin/imagin/internal/digest"
	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/pool"
)

// OCIExporter writes an OCI Image Layout directory.
// The layout follows the OCI Image Specification:
//
//	output/
//	├── oci-layout                 {"imageLayoutVersion": "1.0.0"}
//	├── index.json                 points to manifest(s)
//	└── blobs/sha256/
//	    ├── <config-digest>        image config JSON
//	    ├── <manifest-digest>      manifest JSON
//	    ├── <layer1-digest>        layer 1 tar.gz
//	    └── <layer2-digest>        layer 2 tar.gz
type OCIExporter struct{}

// NewOCIExporter creates a new OCI layout exporter.
func NewOCIExporter() *OCIExporter {
	return &OCIExporter{}
}

// Export writes the OCI Image Layout to outputPath.
func (e *OCIExporter) Export(ctx context.Context, config *types.ImageConfig, layers []*types.Layer, outputPath string) error {
	blobDir := filepath.Join(outputPath, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		return fmt.Errorf("exporter: create blob dir: %w", err)
	}

	// 1. Write oci-layout file
	if err := writeOCILayout(outputPath); err != nil {
		return err
	}

	// 2. Copy layer blobs into the content-addressable store
	layerDescriptors := make([]Descriptor, 0, len(layers))
	for _, layer := range layers {
		if layer.Size == 0 {
			continue // skip empty layers
		}

		blobName := digestHash(string(layer.Digest))
		blobDst := filepath.Join(blobDir, blobName)

		if err := copyBlob(layer.BlobPath, blobDst); err != nil {
			return fmt.Errorf("exporter: copy layer blob: %w", err)
		}

		layerDescriptors = append(layerDescriptors, Descriptor{
			MediaType: layer.MediaType,
			Digest:    string(layer.Digest),
			Size:      layer.Size,
		})
	}

	// 3. Write config blob
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("exporter: marshal config: %w", err)
	}
	configDigest := digest.FromBytes(configJSON)
	configBlobPath := filepath.Join(blobDir, digestHash(configDigest))
	if err := os.WriteFile(configBlobPath, configJSON, 0644); err != nil {
		return fmt.Errorf("exporter: write config blob: %w", err)
	}

	// 4. Create manifest
	manifest := Manifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: Descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    configDigest,
			Size:      int64(len(configJSON)),
		},
		Layers: layerDescriptors,
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("exporter: marshal manifest: %w", err)
	}
	manifestDigest := digest.FromBytes(manifestJSON)
	manifestBlobPath := filepath.Join(blobDir, digestHash(manifestDigest))
	if err := os.WriteFile(manifestBlobPath, manifestJSON, 0644); err != nil {
		return fmt.Errorf("exporter: write manifest blob: %w", err)
	}

	// 5. Write index.json
	index := Index{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    manifestDigest,
				Size:      int64(len(manifestJSON)),
			},
		},
	}

	indexJSON, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("exporter: marshal index: %w", err)
	}
	indexPath := filepath.Join(outputPath, "index.json")
	if err := os.WriteFile(indexPath, indexJSON, 0644); err != nil {
		return fmt.Errorf("exporter: write index.json: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// OCI types (minimal, to avoid external dependency for basic export)
// ---------------------------------------------------------------------------

// Descriptor is an OCI content descriptor.
type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// Manifest is an OCI image manifest.
type Manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

// Index is an OCI image index.
type Index struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Manifests     []Descriptor `json:"manifests"`
}

// writeOCILayout writes the required oci-layout file.
func writeOCILayout(outputPath string) error {
	layout := struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}{
		ImageLayoutVersion: "1.0.0",
	}
	data, err := json.Marshal(layout)
	if err != nil {
		return fmt.Errorf("exporter: marshal oci-layout: %w", err)
	}
	return os.WriteFile(filepath.Join(outputPath, "oci-layout"), data, 0644)
}

// copyBlob copies a file from src to dst using a pooled buffer.
func copyBlob(src, dst string) error {
	// Skip if already exists (content-addressable = idempotent)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	buf := pool.GetLargeBuffer()
	defer pool.PutLargeBuffer(buf)

	_, err = io.CopyBuffer(dstFile, srcFile, *buf)
	return err
}

// digestHash extracts the hash portion from "sha256:abc123..." → "abc123..."
func digestHash(d string) string {
	for i := 0; i < len(d); i++ {
		if d[i] == ':' {
			return d[i+1:]
		}
	}
	return d
}
