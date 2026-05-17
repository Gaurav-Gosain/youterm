//go:build darwin

package player

import "os"

func tmpDir() string { return os.TempDir() }

func setPipeSize(_ *os.File, _ int) {
	// F_SETPIPE_SZ is Linux-only; no-op on macOS
}
