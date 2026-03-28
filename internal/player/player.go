package player

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/term"
)

type Config struct {
	URL       string
	Title     string
	Duration  float64
	MaxHeight int
	FPS       int
}

type player struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	tty      *os.File
	ttyFd    int
	oldState *term.State

	vw, vh int // video decode dimensions

	// Direct CDN URLs
	videoURL  string
	audioURL  string
	sourceFPS float64

	// Processes
	mu      sync.Mutex
	ffmpeg  *exec.Cmd
	ffmpegA *exec.Cmd
	aplay   *exec.Cmd
	videoR  *os.File

	// Audio sync: bytes pumped to audio player
	audioBytesWritten atomic.Int64
	hasAudio          atomic.Bool

	// Frame rendering — mmap'd file for zero-copy
	framePath    string
	framePathB64 string
	frameFd      int
	frameMmap    []byte
	frameSize int

	// Cached terminal size (updated on SIGWINCH)
	tw, th       atomic.Int32
	cellW, cellH atomic.Int64 // stored as fixed-point * 1000

	quit     chan struct{}
	position float64 // seek offset
	paused   atomic.Bool
	pauseT   time.Time
	seekCh   chan float64
	dirty    atomic.Bool // set on resize to trigger full screen clear

	// Pre-built escape parts
	kittyHdr string
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func getWinsize(fd int) winsize {
	var ws winsize
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	return ws
}

func Run(cfg Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	p := &player{
		cfg:     cfg,
		ctx:     ctx,
		cancel:  cancel,
		quit:    make(chan struct{}),
		seekCh:  make(chan float64, 1),
		frameFd: -1,
	}

	if err := p.init(); err != nil {
		p.cleanup()
		return err
	}
	defer p.cleanup()
	return p.loop()
}

func (p *player) init() error {
	p.vw, p.vh = videoSize(p.cfg.MaxHeight)
	p.frameSize = p.vw * p.vh * 3

	// Create mmap'd frame file on tmpfs (Linux) or tempdir (macOS)
	p.framePath = fmt.Sprintf("%s/youterm-%d", tmpDir(), os.Getpid())
	p.framePathB64 = base64.StdEncoding.EncodeToString([]byte(p.framePath))

	fd, err := syscall.Open(p.framePath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("creating frame file: %w", err)
	}
	if err := syscall.Ftruncate(fd, int64(p.frameSize)); err != nil {
		syscall.Close(fd)
		return fmt.Errorf("truncating frame file: %w", err)
	}
	mmap, err := syscall.Mmap(fd, 0, p.frameSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		syscall.Close(fd)
		return fmt.Errorf("mmap frame file: %w", err)
	}
	p.frameFd = fd
	p.frameMmap = mmap

	// Pre-build static kitty escape header
	// Pre-build static kitty escape header
	p.kittyHdr = fmt.Sprintf("\x1b_Ga=T,t=f,f=24,s=%d,v=%d,", p.vw, p.vh)

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening /dev/tty: %w", err)
	}
	p.tty = tty
	p.ttyFd = int(tty.Fd())

	oldState, err := term.MakeRaw(p.ttyFd)
	if err != nil {
		return fmt.Errorf("entering raw mode: %w", err)
	}
	p.oldState = oldState

	_, _ = tty.WriteString("\x1b[?1049h\x1b[?25l")

	p.updateWinsize()

	p.writeMessage("Resolving stream URLs...")

	if err := p.resolveURLs(); err != nil {
		return fmt.Errorf("resolving URLs: %w", err)
	}

	p.writeMessage("Buffering...")

	if err := p.startAt(0); err != nil {
		return err
	}

	go p.handleKeys()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			p.updateWinsize()
		}
	}()

	return nil
}

func (p *player) updateWinsize() {
	ws := getWinsize(p.ttyFd)
	tw := int32(ws.Col)
	th := int32(ws.Row)
	if tw == 0 || th == 0 {
		tw, th = 160, 40
	}
	p.tw.Store(tw)
	p.th.Store(th)
	if ws.Xpixel > 0 && ws.Col > 0 {
		p.cellW.Store(int64(float64(ws.Xpixel) / float64(ws.Col) * 1000))
	}
	if ws.Ypixel > 0 && ws.Row > 0 {
		p.cellH.Store(int64(float64(ws.Ypixel) / float64(ws.Row) * 1000))
	}
	p.dirty.Store(true)
}

func (p *player) resolveURLs() error {
	h := p.cfg.MaxHeight
	format := fmt.Sprintf(
		"bestvideo[height<=%d]+bestaudio/best[height<=%d]/best",
		h, h,
	)

	// Resolve URLs and FPS in parallel
	type result struct {
		urls string
		fps  string
		err  error
	}
	urlCh := make(chan result, 1)
	fpsCh := make(chan result, 1)

	go func() {
		out, err := exec.CommandContext(p.ctx, "yt-dlp",
			"-g", "-f", format,
			"--no-warnings", "--no-playlist",
			p.cfg.URL,
		).Output()
		urlCh <- result{urls: string(out), err: err}
	}()

	go func() {
		out, _ := exec.CommandContext(p.ctx, "yt-dlp",
			"--print", "fps",
			"-f", fmt.Sprintf("bestvideo[height<=%d]/best[height<=%d]/best", h, h),
			"--no-warnings", "--no-playlist",
			p.cfg.URL,
		).Output()
		fpsCh <- result{fps: string(out)}
	}()

	urlRes := <-urlCh
	if urlRes.err != nil {
		return fmt.Errorf("yt-dlp -g: %w", urlRes.err)
	}

	urls := strings.Split(strings.TrimSpace(urlRes.urls), "\n")
	if len(urls) >= 2 {
		p.videoURL = urls[0]
		p.audioURL = urls[1]
	} else if len(urls) == 1 && urls[0] != "" {
		p.videoURL = urls[0]
		p.audioURL = urls[0]
	} else {
		return fmt.Errorf("yt-dlp returned no URLs")
	}

	fpsRes := <-fpsCh
	p.sourceFPS = 30
	if fps := strings.TrimSpace(fpsRes.fps); fps != "" {
		if _, err := fmt.Sscanf(fps, "%f", &p.sourceFPS); err != nil {
			p.sourceFPS = 30
		}
	}
	if p.sourceFPS <= 0 {
		p.sourceFPS = 30
	}

	return nil
}

// startAt launches video ffmpeg first. Audio pipeline is started later
// by startAudio() when the first video frame is decoded, ensuring
// audio never plays before video is visible.
func (p *player) startAt(offset float64) error {
	p.stopProcesses()

	if offset < 0 {
		offset = 0
	}
	if p.cfg.Duration > 0 && offset > p.cfg.Duration {
		offset = p.cfg.Duration
	}

	ssArg := fmt.Sprintf("%.1f", offset)

	// --- Video pipeline only ---
	videoR, videoW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("creating video pipe: %w", err)
	}
	// Size pipe to exactly one frame to avoid latency from buffered frames
	setPipeSize(videoR, p.frameSize)

	ffmpeg := exec.CommandContext(p.ctx, "ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		// Low-latency flags for faster startup
		"-probesize", "100000",
		"-analyzeduration", "500000",
		"-fflags", "+nobuffer+fastseek+flush_packets",
		"-flags", "low_delay",
		"-ss", ssArg,
		"-i", p.videoURL,
		"-an", "-map", "0:v:0",
		"-vf", fmt.Sprintf("scale=%d:%d", p.vw, p.vh),
		"-pix_fmt", "rgb24",
		// CFR with explicit FPS so frame counting is accurate
		"-fps_mode", "cfr",
		"-r", fmt.Sprintf("%.2f", p.sourceFPS),
		"-f", "rawvideo", "pipe:1",
	)
	ffmpeg.Stdout = videoW

	if err := ffmpeg.Start(); err != nil {
		videoR.Close()
		videoW.Close()
		return fmt.Errorf("starting video ffmpeg: %w", err)
	}
	videoW.Close()

	p.mu.Lock()
	p.ffmpeg = ffmpeg
	p.videoR = videoR
	p.position = offset
	p.audioBytesWritten.Store(0)
	p.hasAudio.Store(false)
	p.mu.Unlock()

	go func() { _ = ffmpeg.Wait() }()

	return nil
}

// startAudio launches the audio ffmpeg + platform audio player pipeline.
// Called when the first video frame is decoded so audio and video start together.
func (p *player) startAudio() {
	ssArg := fmt.Sprintf("%.1f", p.position)

	audioR, audioW, err := os.Pipe()
	if err != nil {
		return
	}

	ffmpegA := exec.CommandContext(p.ctx, "ffmpeg",
		"-y", "-hide_banner", "-loglevel", "error",
		"-probesize", "50000",
		"-analyzeduration", "200000",
		"-fflags", "+nobuffer+fastseek+flush_packets",
		"-ss", ssArg,
		"-i", p.audioURL,
		"-vn", "-map", "0:a:0",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "pipe:1",
	)
	ffmpegA.Stdout = audioW

	if err := ffmpegA.Start(); err != nil {
		audioR.Close()
		audioW.Close()
		return
	}
	audioW.Close()

	// Create platform audio player with minimal pipe buffer
	aplayR, aplayW, err := os.Pipe()
	if err != nil {
		go func() { _, _ = io.Copy(io.Discard, audioR); audioR.Close() }()
		go func() { _ = ffmpegA.Wait() }()
		p.mu.Lock()
		p.ffmpegA = ffmpegA
		p.mu.Unlock()
		return
	}

	// Minimize OS pipe buffer for tighter sync
	setPipeSize(aplayW, 4096)

	playerBin, playerArgs := audioPlayerCmd()
	aplayCmd := exec.CommandContext(p.ctx, playerBin, playerArgs...)
	aplayCmd.Stdin = aplayR

	if err := aplayCmd.Start(); err != nil {
		aplayR.Close()
		aplayW.Close()
		go func() { _, _ = io.Copy(io.Discard, audioR); audioR.Close() }()
		go func() { _ = ffmpegA.Wait() }()
		p.mu.Lock()
		p.ffmpegA = ffmpegA
		p.mu.Unlock()
		return
	}
	aplayR.Close()

	// Audio pump: count bytes written for sync
	p.audioBytesWritten.Store(0)
	p.hasAudio.Store(true)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := audioR.Read(buf)
			if n > 0 {
				if _, werr := aplayW.Write(buf[:n]); werr != nil {
					break
				}
				p.audioBytesWritten.Add(int64(n))
			}
			if err != nil {
				break
			}
		}
		aplayW.Close()
		audioR.Close()
	}()
	go func() { _ = aplayCmd.Wait() }()
	go func() { _ = ffmpegA.Wait() }()

	p.mu.Lock()
	p.ffmpegA = ffmpegA
	p.aplay = aplayCmd
	p.mu.Unlock()
}

func (p *player) stopProcesses() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, cmd := range []*exec.Cmd{p.aplay, p.ffmpegA, p.ffmpeg} {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGCONT)
			_ = cmd.Process.Kill()
		}
	}
	p.ffmpeg = nil
	p.ffmpegA = nil
	p.aplay = nil

	if p.videoR != nil {
		p.videoR.Close()
		p.videoR = nil
	}

	p.audioBytesWritten.Store(0)
	p.hasAudio.Store(false)
}

const audioBytesPerSec = 48000 * 2 * 2 // 48kHz, 16-bit, stereo

func (p *player) audioPosSeconds() float64 {
	raw := float64(p.audioBytesWritten.Load()) / float64(audioBytesPerSec)
	adj := raw - platformAudioLatency
	if adj < 0 {
		return 0
	}
	return adj
}

func (p *player) loop() error {
	var frameCount int64
	audioStarted := false
	var syncStart time.Time

	for {
		select {
		case <-p.quit:
			return nil
		default:
		}

		select {
		case pos := <-p.seekCh:
			if err := p.startAt(pos); err != nil {
				p.writeMessage(fmt.Sprintf("Seek error: %v [q to quit]", err))
				<-p.quit
				return nil
			}
			frameCount = 0
			audioStarted = false
			continue
		default:
		}

		if p.paused.Load() {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		p.mu.Lock()
		vr := p.videoR
		p.mu.Unlock()

		if vr == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		_, err := io.ReadFull(vr, p.frameMmap)
		if err != nil {
			select {
			case <-p.quit:
				return nil
			default:
			}
			select {
			case pos := <-p.seekCh:
				if startErr := p.startAt(pos); startErr != nil {
					p.writeMessage(fmt.Sprintf("Seek error: %v [q to quit]", startErr))
					<-p.quit
					return nil
				}
				frameCount = 0
				audioStarted = false
				continue
			default:
			}

			p.writeMessage("Playback finished. Press q to quit.")
			<-p.quit
			return nil
		}

		frameCount++

		// First video frame: start audio now so they begin together
		if !audioStarted {
			go p.startAudio()
			syncStart = time.Now()
			frameCount = 1
			audioStarted = true
		}

		// Sync: use audio clock when available, wall clock as fallback
		videoPos := float64(frameCount) / p.sourceFPS

		if p.hasAudio.Load() {
			audioPos := p.audioPosSeconds()

			if videoPos > audioPos+0.02 {
				// Video ahead — wait for audio to catch up
				wait := videoPos - audioPos
				if wait > 0.5 {
					wait = 0.5 // cap wait to avoid hanging
				}
				time.Sleep(time.Duration(wait * float64(time.Second)))
			} else if videoPos < audioPos-0.15 {
				// Video behind — skip this frame
				continue
			}
		} else {
			// No audio yet or no audio available — use wall clock
			targetTime := syncStart.Add(time.Duration(videoPos * float64(time.Second)))
			now := time.Now()
			if targetTime.After(now) {
				time.Sleep(targetTime.Sub(now))
			}
		}

		p.displayFrame()
	}
}

func (p *player) displayFrame() {
	tw := int(p.tw.Load())
	th := int(p.th.Load())

	availRows := max(th-3, 1)
	cols, rows := p.fitImage(tw, availRows)

	// Elapsed time from audio clock
	elapsed := p.position + p.audioPosSeconds()

	var buf bytes.Buffer
	buf.Grow(256)

	// Begin synchronized update
	buf.WriteString("\x1b[?2026h")

	// On resize: clear text rows to remove stale UI artifacts.
	if p.dirty.CompareAndSwap(true, false) {
		for r := 1; r <= th; r++ {
			fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", r)
		}
	}

	// Cursor + kitty graphics
	padLeft := max((tw-cols)/2, 0)
	fmt.Fprintf(&buf, "\x1b[1;%dH%sc=%d,r=%d,C=1,q=2;%s\x1b\\",
		padLeft+1, p.kittyHdr, cols, rows, p.framePathB64)

	// Progress bar
	barRow := max(th-2, 1)
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", barRow)
	if p.cfg.Duration > 0 {
		barW := tw - 2
		if barW > 0 {
			frac := max(0.0, min(elapsed/p.cfg.Duration, 1.0))
			pos := int(frac * float64(barW))
			fmt.Fprintf(&buf, " \x1b[36m%s\x1b[90m%s\x1b[0m",
				strings.Repeat("━", pos),
				strings.Repeat("─", barW-pos))
		}
	}

	// Info line
	infoRow := max(th-1, 1)
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", infoRow)

	icon := "⏸"
	if p.paused.Load() {
		icon = "▶"
	}
	timeStr := formatDuration(elapsed)
	if p.cfg.Duration > 0 {
		timeStr += " / " + formatDuration(p.cfg.Duration)
	}

	title := p.cfg.Title
	maxTitleW := tw - len(timeStr) - 8
	if maxTitleW > 3 && len(title) > maxTitleW {
		title = title[:maxTitleW-3] + "..."
	}
	fmt.Fprintf(&buf, " %s %s  \x1b[90m%s\x1b[0m", icon, title, timeStr)

	// Controls
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K \x1b[90m[space] pause  [←/→] seek  [q] quit\x1b[0m", th)

	// End synchronized update
	buf.WriteString("\x1b[?2026l")

	_, _ = syscall.Write(p.ttyFd, buf.Bytes())
}

func (p *player) fitImage(tw, maxRows int) (cols, rows int) {
	cw := float64(p.cellW.Load()) / 1000.0
	ch := float64(p.cellH.Load()) / 1000.0

	if cw > 0 && ch > 0 {
		availPixW := float64(tw) * cw
		availPixH := float64(maxRows) * ch

		scaleW := availPixW / float64(p.vw)
		scaleH := availPixH / float64(p.vh)
		scale := min(scaleW, scaleH)

		cols = int(float64(p.vw) * scale / cw)
		rows = int(float64(p.vh) * scale / ch)
	} else {
		cols = tw
		arCols := int(float64(maxRows) * float64(p.vw) / float64(p.vh) * 2.0)
		if arCols < cols {
			cols = arCols
		}
		rows = maxRows
	}

	return max(cols, 1), max(rows, 1)
}

func (p *player) handleKeys() {
	buf := make([]byte, 64)
	for {
		n, err := p.tty.Read(buf)
		if err != nil || n == 0 {
			return
		}

		switch {
		case buf[0] == 'q' || buf[0] == 3:
			p.mu.Lock()
			if p.videoR != nil {
				p.videoR.Close()
				p.videoR = nil
			}
			p.mu.Unlock()
			select {
			case <-p.quit:
			default:
				close(p.quit)
			}
			return

		case buf[0] == ' ':
			p.togglePause()

		case n >= 3 && buf[0] == 0x1b && buf[1] == '[':
			switch buf[2] {
			case 'C':
				p.seekRelative(5)
			case 'D':
				p.seekRelative(-5)
			case 'A':
				p.seekRelative(30)
			case 'B':
				p.seekRelative(-30)
			}

		case buf[0] == 'l':
			p.seekRelative(10)
		case buf[0] == 'h':
			p.seekRelative(-10)
		case buf[0] == 'j':
			p.seekRelative(-60)
		case buf[0] == 'k':
			p.seekRelative(60)
		}
	}
}

func (p *player) seekRelative(delta float64) {
	// Use audio clock for current position if available
	var current float64
	if p.hasAudio.Load() {
		current = p.position + p.audioPosSeconds()
	} else {
		current = p.position
	}

	newPos := current + delta
	if newPos < 0 {
		newPos = 0
	}
	if p.cfg.Duration > 0 && newPos > p.cfg.Duration {
		newPos = p.cfg.Duration
	}

	p.mu.Lock()
	if p.paused.Load() {
		p.paused.Store(false)
	}
	if p.videoR != nil {
		p.videoR.Close()
		p.videoR = nil
	}
	p.mu.Unlock()

	select {
	case p.seekCh <- newPos:
	default:
	}
}

func (p *player) togglePause() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.paused.Load() {
		for _, cmd := range []*exec.Cmd{p.ffmpeg, p.ffmpegA, p.aplay} {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGCONT)
			}
		}
		p.paused.Store(false)
	} else {
		p.paused.Store(true)
		for _, cmd := range []*exec.Cmd{p.ffmpeg, p.ffmpegA, p.aplay} {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGSTOP)
			}
		}
	}
}

func (p *player) writeMessage(msg string) {
	tw := int(p.tw.Load())
	th := int(p.th.Load())
	if tw == 0 {
		tw = 80
	}
	if th == 0 {
		th = 24
	}
	row := th / 2
	col := max((tw-len(msg))/2, 1)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b[2J\x1b[%d;%dH%s", row, col, msg)
	_, _ = syscall.Write(p.ttyFd, buf.Bytes())
}

func (p *player) cleanup() {
	p.cancel()
	p.stopProcesses()

	if p.tty != nil {
		_, _ = p.tty.WriteString("\x1b_Ga=d;\x1b\\\x1b[?25h\x1b[?1049l")
	}

	if p.oldState != nil && p.tty != nil {
		_ = term.Restore(p.ttyFd, p.oldState)
		p.oldState = nil
	}

	if p.tty != nil {
		p.tty.Close()
		p.tty = nil
	}

	if p.frameMmap != nil {
		_ = syscall.Munmap(p.frameMmap)
		p.frameMmap = nil
	}
	if p.frameFd >= 0 {
		syscall.Close(p.frameFd)
		p.frameFd = -1
	}
	os.Remove(p.framePath)
}

func videoSize(maxHeight int) (int, int) {
	switch {
	case maxHeight >= 1080:
		return 1920, 1080
	case maxHeight >= 720:
		return 1280, 720
	default:
		return 854, 480
	}
}

func formatDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
