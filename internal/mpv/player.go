package mpv

import (
	"fmt"
	"os"
	"os/exec"
)

func Play(url string, maxHeight int) error {
	if _, err := exec.LookPath("mpv"); err != nil {
		return fmt.Errorf("mpv not found in PATH (install mpv with kitty graphics support)")
	}

	format := fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best[height<=%d]/best", maxHeight, maxHeight)

	args := []string{
		"--vo=kitty",
		"--vo-kitty-use-shm=yes",
		"--profile=sw-fast",
		"--ytdl-format=" + format,
		"--really-quiet",
		url,
	}

	cmd := exec.Command("mpv", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 4 {
				return nil
			}
		}
		return fmt.Errorf("mpv: %w", err)
	}
	return nil
}
