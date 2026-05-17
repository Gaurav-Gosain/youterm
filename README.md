<div align="center">
  <h1>youterm</h1>
  <p>Play YouTube videos in your terminal using the kitty graphics protocol.</p>

  <a href="https://github.com/Gaurav-Gosain/youterm/releases"><img src="https://img.shields.io/github/release/Gaurav-Gosain/youterm.svg" alt="Latest Release"></a>
  <a href="https://pkg.go.dev/github.com/Gaurav-Gosain/youterm?tab=doc"><img src="https://godoc.org/github.com/Gaurav-Gosain/youterm?status.svg" alt="GoDoc"></a>
  <a href="https://deepwiki.com/Gaurav-Gosain/youterm"><img src="https://deepwiki.com/badge.svg" alt="Ask DeepWiki"></a>
</div>

---

youterm streams YouTube videos in terminals that support the [kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/). It pairs an interactive TUI picker with a native renderer: thumbnail-driven search, mouse-driven seek bar with buffer indicator, on-the-fly quality switching, and synchronized audio via the platform's native sound API.

<details>
<summary>Table of Contents</summary>

- [Installation](#installation)
- [Usage](#usage)
- [Features](#features)
- [Controls](#controls)
- [Development](#development)
- [License](#license)

</details>

## Installation

### Go Install

```bash
go install github.com/Gaurav-Gosain/youterm/cmd/youterm@latest
```

### Other Methods

- **[GitHub Releases](https://github.com/Gaurav-Gosain/youterm/releases)** - pre-built binaries
- **Build from source:** see [Development](#development)

### Prerequisites

- A terminal with kitty graphics support: [kitty](https://sw.kovidgoyal.net/kitty/), [Ghostty](https://ghostty.org/), [WezTerm](https://wezfurlong.org/wezterm/)
- [yt-dlp](https://github.com/yt-dlp/yt-dlp) on `PATH` (stream resolution)
- [ffmpeg](https://ffmpeg.org/) on `PATH` (decoding)
- Audio backend (auto-detected via [oto](https://github.com/ebitengine/oto)):
  - Linux: ALSA or PulseAudio
  - macOS: CoreAudio (built in)
- Optional: [mpv](https://mpv.io/) for the `--mpv` fallback renderer

Currently supported platforms: **Linux**, **macOS**. Windows requires extra work and is not supported yet.

## Usage

```bash
# Open the picker (no args)
youterm

# Search and play
youterm lofi hip hop

# Play a direct URL
youterm https://www.youtube.com/watch?v=dQw4w9WgXcQ

# Pick a target resolution and frame rate
youterm -q 720 -fps 60 "never gonna give you up"

# Use mpv backend instead of the native renderer
youterm --mpv https://www.youtube.com/watch?v=dQw4w9WgXcQ
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-q` | `480` | Max video height (360, 480, 720, 1080) |
| `-fps` | `30` | Target frame rate |
| `-n` | `10` | Number of search results |
| `-mpv` | `false` | Use mpv backend instead of native renderer |

## Features

- **Native terminal rendering** via kitty graphics, zero-copy through an mmap'd frame buffer
- **Async decoder** with a 150-frame ring buffer for buffer-ahead playback
- **Native audio** through CoreAudio / ALSA / PulseAudio via [oto](https://github.com/ebitengine/oto) (no external audio process)
- **Interactive picker** with integrated search bar and thumbnail previews
- **YouTube-style seek bar** with red played portion, dim buffered portion, and a position dot
- **Mouse support:** click on video to pause, click and drag on the seek bar to scrub, scroll wheel to seek
- **In-buffer seek** skips frames instead of re-downloading when seeking forward within the buffer
- **On-the-fly quality switching** (1-4 keys) with seamless restart at the current position
- **Loop** mode, **mute**, **back to search** from the player
- **Aspect-ratio aware** for portrait videos and Shorts

## Controls

### Picker

| Key | Action |
|-----|--------|
| Type | Search YouTube |
| `Enter` | Run search / play selected |
| `j` / `k` / arrows | Navigate results |
| `g` / `G` | Top / bottom |
| `/` | Focus search bar |
| `Esc` | Back from search to results, or quit |
| `q` / `Ctrl-C` | Quit |

### Player

| Key | Action |
|-----|--------|
| `Space` | Pause / resume |
| Click on video | Pause / resume |
| `f` / Right-click | Hide / show bottom bar |
| `Left` / `Right` | Seek -5s / +5s |
| `h` / `l` | Seek -10s / +10s |
| `Up` / `Down` | Seek +30s / -30s |
| `j` / `k` | Seek -60s / +60s |
| Scroll wheel | Seek -5s / +5s |
| Click + drag bar | Scrub preview, seek on release |
| `m` | Mute / unmute |
| `r` | Toggle loop |
| `1` / `2` / `3` / `4` | Switch quality (360p / 480p / 720p / 1080p) |
| `/` | Back to search |
| `q` / `Ctrl-C` | Quit |

## Development

Contributions welcome. Open issues or pull requests.

```bash
git clone https://github.com/Gaurav-Gosain/youterm.git
cd youterm
go build -o youterm ./cmd/youterm
./youterm --help
```

**Support:** [![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/B0B81N8V1R)

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=Gaurav-Gosain/youterm&type=Date&theme=dark)](https://star-history.com/#Gaurav-Gosain/youterm&Date)

<p style="display:flex;flex-wrap:wrap;">
<img alt="GitHub Language Count" src="https://img.shields.io/github/languages/count/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Top Language" src="https://img.shields.io/github/languages/top/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="Repo Size" src="https://img.shields.io/github/repo-size/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Issues" src="https://img.shields.io/github/issues/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Closed Issues" src="https://img.shields.io/github/issues-closed/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Pull Requests" src="https://img.shields.io/github/issues-pr/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Closed Pull Requests" src="https://img.shields.io/github/issues-pr-closed/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Contributors" src="https://img.shields.io/github/contributors/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
<img alt="GitHub Last Commit" src="https://img.shields.io/github/last-commit/Gaurav-Gosain/youterm" style="padding:5px;margin:5px;" />
</p>

## License

MIT. See [LICENSE](LICENSE).
