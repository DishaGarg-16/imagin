package exporter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/snapshotter"
)

func createTestLayer(t *testing.T) (*types.Layer, string) {
	t.Helper()

	blobDir, err := os.MkdirTemp("", "blob-*")
	if err != nil {
		t.Fatal(err)
	}

	upperDir, err := os.MkdirTemp("", "upper-*")
	if err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(upperDir, "test.txt"), []byte("test content"), 0644)

	snap, err := snapshotter.New(blobDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	layer, err := snap.Snapshot(context.Background(), upperDir, "RUN echo test")
	if err != nil {
		t.Fatal(err)
	}

	os.RemoveAll(upperDir)
	return layer, blobDir
}

func TestOCIExport(t *testing.T) {
	layer, blobDir := createTestLayer(t)
	defer os.RemoveAll(blobDir)

	outputDir, err := os.MkdirTemp("", "oci-export-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outputDir)

	config := &types.ImageConfig{
		Architecture: "amd64",
		OS:           "linux",
		Config: types.ContainerConfig{
			Cmd: []string{"/bin/sh"},
			Env: []string{"PATH=/usr/bin"},
		},
		RootFS: types.RootFSConfig{
			Type:    "layers",
			DiffIDs: []string{string(layer.DiffID)},
		},
		History: []types.HistoryEntry{
			{CreatedBy: "RUN echo test", Created: time.Now()},
		},
		Created: time.Now(),
	}

	exporter := NewOCIExporter()
	if err := exporter.Export(context.Background(), config, []*types.Layer{layer}, outputDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify oci-layout exists
	layoutData, err := os.ReadFile(filepath.Join(outputDir, "oci-layout"))
	if err != nil {
		t.Fatal("oci-layout missing")
	}
	if !strings.Contains(string(layoutData), "1.0.0") {
		t.Error("oci-layout should contain version 1.0.0")
	}

	// Verify index.json
	indexData, err := os.ReadFile(filepath.Join(outputDir, "index.json"))
	if err != nil {
		t.Fatal("index.json missing")
	}
	var index Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatalf("invalid index.json: %v", err)
	}
	if len(index.Manifests) != 1 {
		t.Errorf("expected 1 manifest, got %d", len(index.Manifests))
	}

	// Verify manifest blob exists
	manifestDigest := index.Manifests[0].Digest
	manifestBlobPath := filepath.Join(outputDir, "blobs", "sha256", digestHash(manifestDigest))
	manifestData, err := os.ReadFile(manifestBlobPath)
	if err != nil {
		t.Fatalf("manifest blob missing: %v", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}

	if manifest.SchemaVersion != 2 {
		t.Errorf("expected schema version 2, got %d", manifest.SchemaVersion)
	}
	if len(manifest.Layers) != 1 {
		t.Errorf("expected 1 layer, got %d", len(manifest.Layers))
	}

	// Verify config blob exists
	configBlobPath := filepath.Join(outputDir, "blobs", "sha256", digestHash(manifest.Config.Digest))
	if _, err := os.Stat(configBlobPath); err != nil {
		t.Fatal("config blob missing")
	}

	// Verify layer blob exists
	layerBlobPath := filepath.Join(outputDir, "blobs", "sha256", digestHash(manifest.Layers[0].Digest))
	if _, err := os.Stat(layerBlobPath); err != nil {
		t.Fatal("layer blob missing")
	}
}

func TestTarExport(t *testing.T) {
	layer, blobDir := createTestLayer(t)
	defer os.RemoveAll(blobDir)

	outputFile := filepath.Join(os.TempDir(), "test-export.tar")
	defer os.Remove(outputFile)

	config := &types.ImageConfig{
		Architecture: "amd64",
		OS:           "linux",
		Config:       types.ContainerConfig{Cmd: []string{"/bin/sh"}},
		RootFS: types.RootFSConfig{
			Type:    "layers",
			DiffIDs: []string{string(layer.DiffID)},
		},
		Created: time.Now(),
	}

	exporter := NewTarExporter()
	if err := exporter.Export(context.Background(), config, []*types.Layer{layer}, outputFile); err != nil {
		t.Fatalf("Tar export failed: %v", err)
	}

	// Verify tar file exists and has content
	info, err := os.Stat(outputFile)
	if err != nil {
		t.Fatal("tar file missing")
	}
	if info.Size() == 0 {
		t.Error("tar file is empty")
	}
}
