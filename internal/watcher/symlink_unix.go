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
	return deviceInode{dev: uint64(stat.Dev), ino: stat.Ino}, nil
}
