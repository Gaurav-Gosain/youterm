//go:build linux

package player

import (
	"os"
	"syscall"
)

func tmpDir() string { return "/dev/shm" }

func audioPlayerCmd() (string, []string) {
	return "aplay", []string{
		"-f", "S16_LE", "-r", "48000", "-c", "2", "-t", "raw", "-q",
		"--buffer-time", "30000", "--period-time", "5000",
	}
}

// aplay 30ms buffer + ~21ms pipe buffer at 4096 bytes
const platformAudioLatency = 0.051

func setPipeSize(f *os.File, size int) {
	syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETPIPE_SZ, uintptr(size))
}
