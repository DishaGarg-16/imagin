package rootfs

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// isOverlayAvailable checks if OverlayFS is available on this system.
// OverlayFS requires Linux and typically root privileges.
func isOverlayAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}

	// Check if overlayfs is in /proc/filesystems
	out, err := exec.Command("cat", "/proc/filesystems").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "overlay")
}

// mountOverlay mounts an OverlayFS at info.MergedDir.
//
// OverlayFS works by stacking directories:
//   - lowerdir: one or more read-only directories (colon-separated, rightmost = base)
//   - upperdir: writable directory where all changes are recorded
//   - workdir:  internal work directory (must be on same filesystem as upperdir)
//   - merged:   the unified view where all layers appear combined
//
// When a file is read, overlay checks upperdir first, then lowerdir(s) in order.
// When a file is written, it's copy-on-write'd to upperdir.
// When a file is deleted, a "whiteout" device node is created in upperdir.
func mountOverlay(info *MountInfo) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("overlayfs not available on %s", runtime.GOOS)
	}

	// Build the lowerdir option. Multiple lowerdirs are colon-separated,
	// with the leftmost being the topmost layer.
	lowerOpt := ""
	if len(info.LowerDirs) > 0 {
		lowerOpt = strings.Join(info.LowerDirs, ":")
	} else {
		// Create an empty base if no lower dirs
		lowerOpt = info.MergedDir // self-referencing as empty base
		return fmt.Errorf("overlayfs requires at least one lower dir")
	}

	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
		lowerOpt, info.UpperDir, info.WorkDir)

	cmd := exec.Command("mount", "-t", "overlay", "overlay", "-o", opts, info.MergedDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount overlay failed: %w\noutput: %s", err, string(output))
	}

	return nil
}

// unmountOverlay unmounts the overlay at the given mount info.
func unmountOverlay(info *MountInfo) error {
	if runtime.GOOS != "linux" {
		return nil
	}

	cmd := exec.Command("umount", info.MergedDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount failed: %w\noutput: %s", err, string(output))
	}
	return nil
}
