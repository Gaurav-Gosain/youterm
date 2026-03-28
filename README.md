<div align="center">
  <h1>youterm</h1>
  <p>Play YouTube videos directly in your terminal using the kitty graphics protocol.</p>

  <a href="https://github.com/Gaurav-Gosain/youterm/releases"><img src="https://img.shields.io/github/release/Gaurav-Gosain/youterm.svg" alt="Latest Release"></a>
  <a href="https://pkg.go.dev/github.com/Gaurav-Gosain/youterm?tab=doc"><img src="https://godoc.org/github.com/Gaurav-Gosain/youterm?status.svg" alt="GoDoc"></a>
  <a href="https://deepwiki.com/Gaurav-Gosain/youterm"><img src="https://deepwiki.com/badge.svg" alt="Ask DeepWiki"></a>
</div>

---

youterm streams and plays YouTube videos natively in terminals that support the [kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/). Search for videos or paste a URL, pick a result from the interactive TUI, and watch it right in your terminal with synchronized audio.

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

### Quick Install

**Go Install:**
```bash
go install github.com/Gaurav-Gosain/youterm/cmd/youterm@latest
```

### Other Methods

- **[GitHub Releases](https://github.com/Gaurav-Gosain/youterm/releases)** - Download pre-built binaries
- **Build from Source:** See [Development](#development) below

**Requirements:**
- A terminal with kitty graphics protocol support ([kitty](https://sw.kovidgoyal.net/kitty/), [Ghostty](https://ghostty.org/), etc.)
- [yt-dlp](https://github.com/yt-dlp/yt-dlp)
- [ffmpeg](https://ffmpeg.org/) & ffplay
- Optional: [mpv](https://mpv.io/) (for `--mpv` mode)

## Usage

```bash
# Search and play
youterm lofi hip hop

# Play a direct URL
youterm https://www.youtube.com/watch?v=dQw4w9WgXcQ

# Custom quality and frame rate
youterm -q 720 -fps 60 "never gonna give you up"

# Use mpv backend instead of native player
youterm --mpv https://www.youtube.com/watch?v=dQw4w9WgXcQ
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-q` | `480` | Max video height (360, 480, 720, 1080) |
| `-fps` | `30` | Target frame rate |
| `-n` | `10` | Number of search results |
| `-mpv` | `false` | Use mpv backend instead of native player |

## Features

- **Native terminal rendering** using the kitty graphics protocol with zero-copy frame delivery via shared memory
- **Interactive search** with a scrollable TUI picker powered by Bubble Tea
- **Audio/video sync** with automatic frame skipping to maintain sync
- **Synchronized output** for tear-free rendering
- **Dynamic resizing** that adapts to terminal size changes
- **Seeking** with keyboard shortcuts (5s, 10s, 30s, 60s jumps)
- **Pause/resume** support
- **mpv fallback** for terminals without kitty graphics support

## Controls

### Player

| Key | Action |
|-----|--------|
| `Space` | Pause / Resume |
| `Left` / `Right` | Seek -5s / +5s |
| `h` / `l` | Seek -10s / +10s |
| `Down` / `Up` | Seek -30s / +30s |
| `j` / `k` | Seek -60s / +60s |
| `q` | Quit |

### Search Picker

| Key | Action |
|-----|--------|
| `j` / `Down` | Move cursor down |
| `k` / `Up` | Move cursor up |
| `g` / `G` | Go to top / bottom |
| `Enter` | Play selected video |
| `q` / `Esc` | Quit |

## Development

Contributions are welcome. Feel free to open issues or pull requests.

**Build from source:**
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

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
