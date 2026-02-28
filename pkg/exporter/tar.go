package exporter

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/imagin/imagin/internal/types"
	"github.com/imagin/imagin/pkg/pool"
)

// TarExporter writes the OCI Image Layout as a single .tar file.
// Useful when you need a portable, single-file output.
type TarExporter struct {
	ociExporter *OCIExporter
}

// NewTarExporter creates a new tar exporter.
func NewTarExporter() *TarExporter {
	return &TarExporter{
		ociExporter: NewOCIExporter(),
	}
}

// Export writes the OCI image as a tar file at outputPath.
// It first creates the OCI layout in a temp dir, then archives it.
func (e *TarExporter) Export(ctx context.Context, config *types.ImageConfig, layers []*types.Layer, outputPath string) error {
	// Create OCI layout in a temp directory
	tmpDir, err := os.MkdirTemp("", "imagin-tar-export-*")
	if err != nil {
		return fmt.Errorf("tar exporter: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// First export as OCI layout
	if err := e.ociExporter.Export(ctx, config, layers, tmpDir); err != nil {
		return err
	}

	// Now tar the entire layout
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("tar exporter: create output file: %w", err)
	}
	defer outFile.Close()

	tw := tar.NewWriter(outFile)
	defer tw.Close()

	buf := pool.GetLargeBuffer()
	defer pool.PutLargeBuffer(buf)

	return filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path for the tar entry
		rel, err := filepath.Rel(tmpDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Normalise to forward slashes
		rel = filepath.ToSlash(rel)

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.CopyBuffer(tw, f, *buf)
		return err
	})
}
