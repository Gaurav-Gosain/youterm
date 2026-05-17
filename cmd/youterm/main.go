package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Gaurav-Gosain/youterm/internal/mpv"
	"github.com/Gaurav-Gosain/youterm/internal/player"
	"github.com/Gaurav-Gosain/youterm/internal/ui"
	"github.com/Gaurav-Gosain/youterm/internal/ytdlp"
)

func main() {
	quality := flag.Int("q", 480, "max video height (360, 480, 720, 1080)")
	fps := flag.Int("fps", 30, "target frame rate")
	count := flag.Int("n", 10, "number of search results")
	useMpv := flag.Bool("mpv", false, "use mpv backend instead of native player")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: youterm [options] [youtube-url or search query]

Plays YouTube videos in kitty terminal using the kitty graphics protocol.
Requires: ffmpeg, ffplay, yt-dlp, kitty terminal
Optional: mpv (for --mpv mode)

Options:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Controls:
  space    pause/resume
  q        quit
`)
	}
	flag.Parse()

	query := strings.Join(flag.Args(), " ")
	var cachedResults []ytdlp.Result

	for {
		var videoURL, title string
		var duration float64

		switch {
		case query != "" && isURL(query):
			videoURL = query
			if !*useMpv {
				fmt.Fprintf(os.Stderr, "Resolving metadata...\n")
				if info, err := ytdlp.GetMetadata(query); err == nil {
					title = info.Title
					duration = info.Duration
				}
			}
		default:
			results := cachedResults
			if results == nil && query != "" {
				fmt.Fprintf(os.Stderr, "Searching...\n")
				r, err := ytdlp.Search(query, *count)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Search error: %v\n", err)
					os.Exit(1)
				}
				results = r
			}

			chosen, finalResults, err := ui.Pick(results, *count)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if chosen == nil || chosen.URL == "" {
				return
			}

			cachedResults = finalResults
			videoURL = chosen.URL
			title = chosen.Title
			duration = chosen.Duration
		}

		var err error
		if *useMpv {
			err = mpv.Play(videoURL, *quality)
		} else {
			err = player.Run(player.Config{
				URL:       videoURL,
				Title:     title,
				Duration:  duration,
				MaxHeight: *quality,
				FPS:       *fps,
			})
		}

		if errors.Is(err, player.ErrBackToSearch) {
			query = ""
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
