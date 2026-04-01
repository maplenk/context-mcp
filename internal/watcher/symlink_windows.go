//go:build windows

package watcher

import (
	"fmt"
	"os"
)

// platformDeviceInode returns an error on Windows since syscall.Stat_t is not available.
// Cycle detection on Windows falls back to path-based resolution via filepath.EvalSymlinks.
func platformDeviceInode(info os.FileInfo) (deviceInode, error) {
	return deviceInode{}, fmt.Errorf("inode-based cycle detection not supported on Windows")
}
