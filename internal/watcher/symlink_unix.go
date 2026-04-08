//go:build !windows

package watcher

import (
	"fmt"
	"os"
	"syscall"
)

// platformDeviceInode extracts device and inode numbers from FileInfo on Unix systems.
func platformDeviceInode(info os.FileInfo) (deviceInode, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return deviceInode{}, fmt.Errorf("cannot extract stat info")
	}
	if stat.Dev < 0 {
		return deviceInode{}, fmt.Errorf("device number cannot be negative: %d", stat.Dev)
	}
	// #nosec G115 -- stat.Dev is validated non-negative before widening to uint64.
	return deviceInode{dev: uint64(stat.Dev), ino: stat.Ino}, nil
}
