//go:build darwin

package player

import "os"

func tmpDir() string { return os.TempDir() }

func audioPlayerCmd() (string, []string) {
	// ffplay is bundled with ffmpeg (already a dependency)
	return "ffplay", []string{
		"-nodisp", "-autoexit", "-loglevel", "error",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:0",
	}
}

// ffplay ~100ms buffer + ~83ms pipe buffer (macOS default 16KB pipe)
const platformAudioLatency = 0.183

func setPipeSize(_ *os.File, _ int) {
	// F_SETPIPE_SZ is Linux-only; no-op on macOS
}
