package snapshotter

import (
	"os"
	"path/filepath"
	"strings"
)

// ChangeKind indicates how a file was modified.
type ChangeKind int

const (
	ChangeAdd    ChangeKind = iota // file was added
	ChangeModify                   // file was modified
	ChangeDelete                   // file was deleted (whiteout)
)

// Change represents a single filesystem change.
type Change struct {
	Path string      // relative path within the root
	Kind ChangeKind  // add, modify, or delete
	Info os.FileInfo // nil for deletes
}

// DiffDir walks the upper directory from an overlay mount and produces
// a list of changes. With OverlayFS, the upper dir *IS* the diff — every
// file present is either added or modified, and whiteout files (.wh.*)
// represent deletions.
//
// For the copy-based fallback, we treat the upper dir the same way: any
// file written by the build step appears here.
func DiffDir(upperDir string) ([]Change, error) {
	// Pre-allocate for typical layer sizes
	changes := make([]Change, 0, 64)

	err := filepath.Walk(upperDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if path == upperDir {
			return nil
		}

		rel, err := filepath.Rel(upperDir, path)
		if err != nil {
			return err
		}

		// Normalise path separators to forward slash (OCI standard)
		rel = filepath.ToSlash(rel)

		// Check for OCI whiteout files
		// .wh.<filename> = deletion of <filename>
		// .wh..wh..opq   = opaque whiteout (directory replacement)
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".wh.") {
			if base == ".wh..wh..opq" {
				// Opaque whiteout: the directory was replaced entirely.
				// The directory itself is an "add" and all parent contents
				// are implicitly deleted.
				dir := filepath.Dir(rel)
				if dir != "." {
					changes = append(changes, Change{
						Path: filepath.ToSlash(dir),
						Kind: ChangeAdd,
						Info: info,
					})
				}
			} else {
				// Regular whiteout: specific file was deleted
				deletedName := strings.TrimPrefix(base, ".wh.")
				deletedPath := filepath.ToSlash(filepath.Join(filepath.Dir(rel), deletedName))
				changes = append(changes, Change{
					Path: deletedPath,
					Kind: ChangeDelete,
					Info: nil,
				})
			}
			return nil
		}

		// Regular file or directory — it was added or modified.
		// (In overlay upper-dir semantics, we can't distinguish add vs modify
		//  without comparing to lower, but for tar layer purposes both are the
		//  same: include the file in the layer.)
		changes = append(changes, Change{
			Path: rel,
			Kind: ChangeAdd, // treat all as adds for tar purposes
			Info: info,
		})

		return nil
	})

	return changes, err
}

// DiffTwoDirs computes the diff between an "old" and "new" directory tree.
// Used in fallback mode when we don't have an explicit upper dir.
// Returns changes needed to transform old → new.
func DiffTwoDirs(oldDir, newDir string) ([]Change, error) {
	changes := make([]Change, 0, 64)

	// Map of files in old dir
	oldFiles := make(map[string]os.FileInfo, 128)
	filepath.Walk(oldDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == oldDir {
			return err
		}
		rel, _ := filepath.Rel(oldDir, path)
		oldFiles[filepath.ToSlash(rel)] = info
		return nil
	})

	// Walk new dir and compare
	filepath.Walk(newDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == newDir {
			return err
		}
		rel, _ := filepath.Rel(newDir, path)
		rel = filepath.ToSlash(rel)

		oldInfo, existed := oldFiles[rel]
		if !existed {
			// New file
			changes = append(changes, Change{Path: rel, Kind: ChangeAdd, Info: info})
		} else {
			// Check if modified (different size or modtime)
			if !info.IsDir() && (info.Size() != oldInfo.Size() || info.ModTime() != oldInfo.ModTime()) {
				changes = append(changes, Change{Path: rel, Kind: ChangeModify, Info: info})
			}
			delete(oldFiles, rel) // mark as seen
		}
		return nil
	})

	// Remaining oldFiles entries are deletions
	for rel := range oldFiles {
		changes = append(changes, Change{Path: rel, Kind: ChangeDelete, Info: nil})
	}

	return changes, nil
}
