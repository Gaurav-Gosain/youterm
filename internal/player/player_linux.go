//go:build linux

package player

import (
	"os"
	"syscall"
)

func tmpDir() string { return "/dev/shm" }

func setPipeSize(f *os.File, size int) {
	syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETPIPE_SZ, uintptr(size))
}
