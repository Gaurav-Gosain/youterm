package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Result struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Duration  float64 `json:"duration"`
	Channel   string  `json:"channel"`
	ViewCount int64   `json:"view_count"`
}

type StreamInfo struct {
	Title    string
	Duration float64
}

type Metadata struct {
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
}

func Search(query string, count int) ([]Result, error) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return nil, fmt.Errorf("yt-dlp not found in PATH")
	}

	cmd := exec.Command("yt-dlp",
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		fmt.Sprintf("ytsearch%d:%s", count, query),
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("yt-dlp: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("yt-dlp: %w", err)
	}

	var results []Result
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var r Result
		if err := dec.Decode(&r); err != nil {
			continue
		}
		if r.URL == "" && r.ID != "" {
			r.URL = "https://www.youtube.com/watch?v=" + r.ID
		}
		results = append(results, r)
	}
	return results, nil
}

// GetMetadata fetches title and duration without downloading the video.
func GetMetadata(url string) (*StreamInfo, error) {
	ctx := context.Background()
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return nil, fmt.Errorf("yt-dlp not found in PATH")
	}

	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--dump-json",
		"--no-playlist",
		"--no-warnings",
		"--skip-download",
		url,
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("yt-dlp: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("yt-dlp: %w", err)
	}

	var data Metadata
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parsing yt-dlp output: %w", err)
	}

	return &StreamInfo{
		Title:    data.Title,
		Duration: data.Duration,
	}, nil
}

func FormatDuration(secs float64) string {
	if secs <= 0 {
		return "?:??"
	}
	total := int(secs)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func FormatViews(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB views", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM views", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK views", float64(n)/1_000)
	case n > 0:
		return fmt.Sprintf("%d views", n)
	default:
		return ""
	}
}
