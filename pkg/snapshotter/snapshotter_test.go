package snapshotter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "diff-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files simulating overlay upper dir
	os.WriteFile(filepath.Join(tmpDir, "added.txt"), []byte("new file"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "nested.txt"), []byte("nested"), 0644)

	changes, err := DiffDir(tmpDir)
	if err != nil {
		t.Fatalf("DiffDir failed: %v", err)
	}

	if len(changes) < 2 {
		t.Fatalf("expected at least 2 changes, got %d", len(changes))
	}

	// Check that we found the expected files
	foundAdded := false
	foundNested := false
	for _, c := range changes {
		if c.Path == "added.txt" {
			foundAdded = true
		}
		if strings.Contains(c.Path, "nested.txt") {
			foundNested = true
		}
	}
	if !foundAdded {
		t.Error("missing added.txt in changes")
	}
	if !foundNested {
		t.Error("missing nested.txt in changes")
	}
}

func TestDiffDirWhiteout(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "whiteout-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a whiteout file (simulating overlay deletion)
	os.WriteFile(filepath.Join(tmpDir, ".wh.deleted.txt"), nil, 0644)

	changes, err := DiffDir(tmpDir)
	if err != nil {
		t.Fatalf("DiffDir failed: %v", err)
	}

	foundDelete := false
	for _, c := range changes {
		if c.Path == "deleted.txt" && c.Kind == ChangeDelete {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Error("whiteout file should produce a ChangeDelete for deleted.txt")
	}
}

func TestWriteTarLayer(t *testing.T) {
	// Create source directory with some files
	srcDir, err := os.MkdirTemp("", "tar-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(srcDir)

	os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("hello world"), 0644)
	os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("nested content"), 0644)

	// Get the changes
	changes, err := DiffDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}

	// Write the tar layer
	var buf bytes.Buffer
	diffID, distDigest, compSize, _, err := WriteTarLayer(&buf, srcDir, changes)
	if err != nil {
		t.Fatalf("WriteTarLayer failed: %v", err)
	}

	// Verify digests are non-empty
	if !strings.HasPrefix(diffID, "sha256:") {
		t.Errorf("diffID should start with sha256:, got %q", diffID)
	}
	if !strings.HasPrefix(distDigest, "sha256:") {
		t.Errorf("distDigest should start with sha256:, got %q", distDigest)
	}
	if diffID == distDigest {
		t.Error("diffID and distDigest should differ (one is compressed, one is not)")
	}
	if compSize <= 0 {
		t.Error("compressed size should be > 0")
	}

	// Verify we can decompress and read the tar
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	foundFiles := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		foundFiles[hdr.Name] = true
	}

	if !foundFiles["file1.txt"] {
		t.Error("tar missing file1.txt")
	}
	if !foundFiles["subdir/file2.txt"] {
		t.Error("tar missing subdir/file2.txt")
	}
}

func TestSnapshot(t *testing.T) {
	// Create blob dir and source dir
	blobDir, err := os.MkdirTemp("", "blob-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(blobDir)

	upperDir, err := os.MkdirTemp("", "upper-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(upperDir)

	os.WriteFile(filepath.Join(upperDir, "app.bin"), []byte("binary content here"), 0755)

	snap, err := New(blobDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	layer, err := snap.Snapshot(context.Background(), upperDir, "RUN build app")
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if layer.DiffID == "" {
		t.Error("layer DiffID is empty")
	}
	if layer.Digest == "" {
		t.Error("layer Digest is empty")
	}
	if layer.Size <= 0 {
		t.Error("layer Size should be > 0")
	}
	if layer.MediaType != "application/vnd.oci.image.layer.v1.tar+gzip" {
		t.Errorf("unexpected media type: %s", layer.MediaType)
	}
	if layer.CreatedBy != "RUN build app" {
		t.Errorf("unexpected CreatedBy: %s", layer.CreatedBy)
	}

	// Verify blob file exists
	if _, err := os.Stat(layer.BlobPath); err != nil {
		t.Errorf("blob file not found at %s: %v", layer.BlobPath, err)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	snap, err := New(os.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	layer := snap.SnapshotEmpty("ENV FOO=bar")
	if layer.Size != 0 {
		t.Errorf("empty layer should have size 0, got %d", layer.Size)
	}
	if layer.CreatedBy != "ENV FOO=bar" {
		t.Errorf("unexpected CreatedBy: %s", layer.CreatedBy)
	}
}

func TestDiffTwoDirs(t *testing.T) {
	oldDir, _ := os.MkdirTemp("", "old-*")
	newDir, _ := os.MkdirTemp("", "new-*")
	defer os.RemoveAll(oldDir)
	defer os.RemoveAll(newDir)

	// Old has A and B
	os.WriteFile(filepath.Join(oldDir, "a.txt"), []byte("A"), 0644)
	os.WriteFile(filepath.Join(oldDir, "b.txt"), []byte("B"), 0644)

	// New has A (modified), C (added), B is deleted
	os.WriteFile(filepath.Join(newDir, "a.txt"), []byte("A-modified"), 0644)
	os.WriteFile(filepath.Join(newDir, "c.txt"), []byte("C"), 0644)

	changes, err := DiffTwoDirs(oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}

	hasModifiedA := false
	hasAddedC := false
	hasDeletedB := false
	for _, c := range changes {
		switch {
		case c.Path == "a.txt" && c.Kind == ChangeModify:
			hasModifiedA = true
		case c.Path == "c.txt" && c.Kind == ChangeAdd:
			hasAddedC = true
		case c.Path == "b.txt" && c.Kind == ChangeDelete:
			hasDeletedB = true
		}
	}

	if !hasModifiedA {
		t.Error("expected a.txt to be modified")
	}
	if !hasAddedC {
		t.Error("expected c.txt to be added")
	}
	if !hasDeletedB {
		t.Error("expected b.txt to be deleted")
	}
}

func BenchmarkWriteTarLayer(b *testing.B) {
	srcDir, _ := os.MkdirTemp("", "bench-tar-*")
	defer os.RemoveAll(srcDir)

	// Create 100 small files
	for i := 0; i < 100; i++ {
		data := bytes.Repeat([]byte("x"), 1024)
		os.WriteFile(filepath.Join(srcDir, strings.ReplaceAll("file_"+string(rune('0'+i%10))+".txt", " ", "")), data, 0644)
	}

	changes, _ := DiffDir(srcDir)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		WriteTarLayer(io.Discard, srcDir, changes)
	}
}
