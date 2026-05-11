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

// ErrBackToSearch is returned by Run when the user requests to go back to search.
var ErrBackToSearch = fmt.Errorf("back to search")

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
	srcW, srcH int // source video dimensions (for aspect ratio)

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
	frameSize    int

	// Async frame buffer: decoder goroutine fills, display loop drains
	frameFull  chan []byte // decoded frames ready to display
	frameEmpty chan []byte // recycled buffers for decoder to reuse

	// Cached terminal size (updated on SIGWINCH)
	tw, th       atomic.Int32
	cellW, cellH atomic.Int64 // stored as fixed-point * 1000

	quit      chan struct{}
	position  float64 // seek offset
	paused    atomic.Bool
	seekCh    chan float64
	qualityCh chan int
	dirty     atomic.Bool
	muted     atomic.Bool
	looping   atomic.Bool
	wantSearch atomic.Bool
	scrubbing  atomic.Bool
	scrubFrac  atomic.Int64 // position as fraction * 10000
	buffered   atomic.Int64 // furthest decoded position in ms

	// Reusable rendering state
	drawBuf      bytes.Buffer
	qualityLabel string
	kittyHdr     string
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
		seekCh:    make(chan float64, 1),
		qualityCh: make(chan int, 1),
		frameFd: -1,
	}

	if err := p.init(); err != nil {
		p.cleanup()
		return err
	}
	defer p.cleanup()
	if err := p.loop(); err != nil {
		return err
	}
	if p.wantSearch.Load() {
		return ErrBackToSearch
	}
	return nil
}

func (p *player) init() error {
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

	// Alt screen, hide cursor, enable SGR mouse tracking (button-event mode)
	_, _ = tty.WriteString("\x1b[?1049h\x1b[?25l\x1b[?1002h\x1b[?1006h")

	p.updateWinsize()

	p.writeMessage("Resolving stream URLs...")

	if err := p.resolveURLs(); err != nil {
		return fmt.Errorf("resolving URLs: %w", err)
	}

	p.vw, p.vh = p.scaledSize(p.cfg.MaxHeight)
	p.frameSize = p.vw * p.vh * 3
	p.framePath = fmt.Sprintf("%s/youterm-%d", tmpDir(), os.Getpid())
	p.framePathB64 = base64.StdEncoding.EncodeToString([]byte(p.framePath))

	if err := p.setupMmap(); err != nil {
		return err
	}

	p.initFrameQueue()
	p.kittyHdr = fmt.Sprintf("\x1b_Ga=T,t=f,f=24,s=%d,v=%d,", p.vw, p.vh)
	p.qualityLabel = fmt.Sprintf("%dp", p.vh)

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

func (p *player) setupMmap() error {
	if p.frameMmap != nil {
		_ = syscall.Munmap(p.frameMmap)
	}
	if p.frameFd >= 0 {
		syscall.Close(p.frameFd)
	}
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
	return nil
}

const frameQueueSize = 150 // ~5 seconds at 30fps

func (p *player) initFrameQueue() {
	p.frameFull = make(chan []byte, frameQueueSize)
	p.frameEmpty = make(chan []byte, frameQueueSize)
	for range frameQueueSize {
		p.frameEmpty <- make([]byte, p.frameSize)
	}
}

func (p *player) drainFrameQueue() {
	for {
		select {
		case buf := <-p.frameFull:
			if buf != nil {
				p.frameEmpty <- buf
			}
		default:
			return
		}
	}
}

// decoderLoop reads frames from ffmpeg as fast as it produces them,
// filling the frame queue so playback can buffer ahead.
func (p *player) decoderLoop(vr *os.File, startOffset float64) {
	var n int64
	for {
		var buf []byte
		select {
		case buf = <-p.frameEmpty:
			if buf == nil {
				continue
			}
		case <-p.quit:
			return
		}

		_, err := io.ReadFull(vr, buf)
		if err != nil {
			p.frameEmpty <- buf
			// Send nil sentinel to signal EOF (don't close/recreate the channel)
			select {
			case p.frameFull <- nil:
			default:
			}
			return
		}

		n++
		decoded := startOffset + float64(n)/p.sourceFPS
		p.buffered.Store(int64(decoded * 1000))

		select {
		case p.frameFull <- buf:
		case <-p.quit:
			p.frameEmpty <- buf
			return
		}
	}
}

func (p *player) resolveURLs() error {
	// Always fetch the best available quality. Downscaling to the target
	// resolution is handled by ffmpeg, so the stream quality doesn't need
	// to match the decode dimensions. This also avoids issues with
	// portrait/Shorts videos where height/width filters pick wrong streams.
	format := "bestvideo+bestaudio/best"
	videoFmt := "bestvideo/best"

	// Resolve URLs and video info in parallel
	type result struct {
		urls string
		info string
		err  error
	}
	urlCh := make(chan result, 1)
	infoCh := make(chan result, 1)

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
			"--print", "%(fps)s %(width)s %(height)s",
			"-f", videoFmt,
			"--no-warnings", "--no-playlist",
			p.cfg.URL,
		).Output()
		infoCh <- result{info: string(out)}
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

	infoRes := <-infoCh
	p.sourceFPS = 30
	p.srcW, p.srcH = 1920, 1080 // fallback
	if info := strings.TrimSpace(infoRes.info); info != "" {
		var fps float64
		var w, h int
		if _, err := fmt.Sscanf(info, "%f %d %d", &fps, &w, &h); err == nil {
			if fps > 0 {
				p.sourceFPS = fps
			}
			if w > 0 && h > 0 {
				p.srcW, p.srcH = w, h
			}
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
	// Allow ffmpeg to decode ahead by giving the pipe room for multiple frames
	setPipeSize(videoR, p.frameSize*8)

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
	p.buffered.Store(int64(offset * 1000))
	p.mu.Unlock()

	// Drain old frame queue and start decoder goroutine
	p.drainFrameQueue()
	go p.decoderLoop(videoR, offset)

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
		silence := make([]byte, 4096) // zero bytes = silence
		for {
			n, err := audioR.Read(buf)
			if n > 0 {
				var werr error
				if p.muted.Load() {
					_, werr = aplayW.Write(silence[:n])
				} else {
					_, werr = aplayW.Write(buf[:n])
				}
				if werr != nil {
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

func (p *player) stopAudioOnly() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, cmd := range []*exec.Cmd{p.aplay, p.ffmpegA} {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGCONT)
			_ = cmd.Process.Kill()
		}
	}
	p.ffmpegA = nil
	p.aplay = nil
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
	eofCount := 0 // consecutive nil frames received

	reset := func() {
		frameCount = 0
		audioStarted = false
		eofCount = 0
	}

	for {
		// Priority checks: quit > quality > seek
		select {
		case <-p.quit:
			return nil
		default:
		}
		select {
		case maxH := <-p.qualityCh:
			if err := p.changeQuality(maxH); err != nil {
				p.writeMessage(fmt.Sprintf("Quality error: %v", err))
			}
			reset()
			continue
		default:
		}
		select {
		case pos := <-p.seekCh:
			current := p.position + p.audioPosSeconds()
			bufEnd := float64(p.buffered.Load()) / 1000.0

			if pos > current && pos < bufEnd-0.5 && audioStarted {
				// Seek is within the buffer: skip frames instead of restarting
				framesToSkip := int((pos - current) * p.sourceFPS)
				for range framesToSkip {
					select {
					case buf := <-p.frameFull:
						if buf != nil {
							p.frameEmpty <- buf
						}
					default:
					}
				}
				p.stopAudioOnly()
				p.position = pos
				p.audioBytesWritten.Store(0)
				go p.startAudio()
				syncStart = time.Now()
				frameCount = 0
				audioStarted = true
			} else {
				if err := p.startAt(pos); err != nil {
					p.writeMessage(fmt.Sprintf("Seek error: %v", err))
				}
				reset()
			}
			continue
		default:
		}

		// Pause: keep displaying last frame, let buffer fill
		if p.paused.Load() {
			p.displayFrame()
			time.Sleep(50 * time.Millisecond)
			continue
		}

		// Try to read next frame (non-blocking)
		var frame []byte
		select {
		case f := <-p.frameFull:
			frame = f
		case <-p.quit:
			return nil
		default:
			// No frame ready yet, spin back to check pause/seek/quit
			time.Sleep(5 * time.Millisecond)
			continue
		}

		// nil means decoder hit an error (EOF, pipe closed by seek, etc.)
		// Loop back to let the seek/quality handlers at the top process it.
		// Only treat as true EOF after several consecutive nils with no pending actions.
		if frame == nil {
			eofCount++
			if eofCount < 3 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			// Check looping
			if p.looping.Load() {
				if p.startAt(0) == nil {
					reset()
					continue
				}
			}
			// True EOF
			p.writeMessage("Playback finished. [r] replay  [/] search  [q] quit")
			for {
				select {
				case <-p.quit:
					return nil
				case pos := <-p.seekCh:
					if p.startAt(pos) == nil {
						reset()
					}
				case maxH := <-p.qualityCh:
					p.changeQuality(maxH)
					reset()
				default:
				}
				if eofCount == 0 {
					break // was restarted
				}
				if p.looping.Load() {
					p.looping.Store(false)
					if p.startAt(0) == nil {
						reset()
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
			continue
		}

		eofCount = 0
		frameCount++

		if !audioStarted {
			go p.startAudio()
			syncStart = time.Now()
			frameCount = 1
			audioStarted = true
		}

		// Check pause before sync sleep
		if p.paused.Load() {
			copy(p.frameMmap, frame)
			p.frameEmpty <- frame
			p.displayFrame()
			time.Sleep(50 * time.Millisecond)
			continue
		}

		// Sync to audio or wall clock
		videoPos := float64(frameCount) / p.sourceFPS
		if p.hasAudio.Load() {
			audioPos := p.audioPosSeconds()
			if videoPos > audioPos+0.02 {
				wait := min(videoPos-audioPos, 0.1)
				time.Sleep(time.Duration(wait * float64(time.Second)))
			} else if videoPos < audioPos-0.15 {
				p.frameEmpty <- frame
				continue
			}
		} else {
			target := syncStart.Add(time.Duration(videoPos * float64(time.Second)))
			if d := time.Until(target); d > 0 {
				time.Sleep(d)
			}
		}

		copy(p.frameMmap, frame)
		p.frameEmpty <- frame
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

	buf := &p.drawBuf
	buf.Reset()

	// Begin synchronized update
	buf.WriteString("\x1b[?2026h")

	// On resize: delete old kitty placements and clear all text rows.
	if p.dirty.CompareAndSwap(true, false) {
		buf.WriteString("\x1b_Ga=d,d=a,q=2;\x1b\\")
		for r := 1; r <= th; r++ {
			fmt.Fprintf(buf, "\x1b[%d;1H\x1b[2K", r)
		}
	}

	// Cursor + kitty graphics
	padLeft := max((tw-cols)/2, 0)
	fmt.Fprintf(buf, "\x1b[1;%dH%sc=%d,r=%d,C=1,q=2;%s\x1b\\",
		padLeft+1, p.kittyHdr, cols, rows, p.framePathB64)

	// Progress bar (YouTube-style with position dot)
	barRow := max(th-2, 1)
	fmt.Fprintf(buf, "\x1b[%d;1H\x1b[2K", barRow)
	scrubActive := p.scrubbing.Load()
	if p.cfg.Duration > 0 {
		barW := tw - 2
		if barW > 0 {
			playFrac := max(0.0, min(elapsed/p.cfg.Duration, 1.0))
			bufFrac := max(0.0, min(float64(p.buffered.Load())/1000.0/p.cfg.Duration, 1.0))
			pos := min(int(playFrac*float64(barW)), barW)
			bufPos := min(int(bufFrac*float64(barW)), barW)
			if bufPos < pos {
				bufPos = pos
			}

			if scrubActive {
				scrubF := float64(p.scrubFrac.Load()) / 10000.0
				scrubPos := min(int(scrubF*float64(barW)), barW)

				// played | scrub preview | buffered | remaining
				buf.WriteString(" ")
				if scrubPos >= pos {
					buf.WriteString("\x1b[31m")
					buf.WriteString(strings.Repeat("\u2501", max(pos-1, 0)))
					buf.WriteString("\x1b[33m")
					buf.WriteString(strings.Repeat("\u2501", scrubPos-pos))
					buf.WriteString("\x1b[1;33m\u25cf\x1b[0m")
					after := barW - scrubPos
					bufAfter := min(bufPos-scrubPos, after)
					if bufAfter > 0 {
						buf.WriteString("\x1b[2;37m")
						buf.WriteString(strings.Repeat("\u2500", bufAfter))
						after -= bufAfter
					}
					buf.WriteString("\x1b[90m")
					buf.WriteString(strings.Repeat("\u2500", max(after, 0)))
				} else {
					buf.WriteString("\x1b[31m")
					buf.WriteString(strings.Repeat("\u2501", max(scrubPos, 0)))
					buf.WriteString("\x1b[1;33m\u25cf\x1b[0m")
					buf.WriteString("\x1b[90m")
					buf.WriteString(strings.Repeat("\u2500", barW-scrubPos))
				}
				buf.WriteString("\x1b[0m")
			} else {
				// played | dot | buffered | remaining
				played := max(pos-1, 0)
				buf.WriteString(" \x1b[31m")
				buf.WriteString(strings.Repeat("\u2501", played))
				buf.WriteString("\x1b[1;31m\u25cf\x1b[0m")
				buffered := max(bufPos-pos, 0)
				remaining := max(barW-bufPos, 0)
				if buffered > 0 {
					buf.WriteString("\x1b[2;37m")
					buf.WriteString(strings.Repeat("\u2500", buffered))
				}
				buf.WriteString("\x1b[90m")
				buf.WriteString(strings.Repeat("\u2500", remaining))
				buf.WriteString("\x1b[0m")
			}
		}
	}

	// Info line
	infoRow := max(th-1, 1)
	fmt.Fprintf(buf, "\x1b[%d;1H\x1b[2K", infoRow)

	icon := "\uf04c" // nf-fa-pause
	if p.paused.Load() {
		icon = "\uf04b" // nf-fa-play
	}
	if p.muted.Load() {
		icon += " \uf026" // nf-fa-volume_off
	}
	if p.looping.Load() {
		icon += " \uf01e" // nf-fa-repeat
	}

	qualityLabel := p.qualityLabel
	var timeStr string
	if p.scrubbing.Load() && p.cfg.Duration > 0 {
		scrubF := float64(p.scrubFrac.Load()) / 10000.0
		timeStr = "\x1b[33m" + formatDuration(scrubF*p.cfg.Duration) + "\x1b[0;90m / " + formatDuration(p.cfg.Duration)
	} else {
		timeStr = formatDuration(elapsed)
		if p.cfg.Duration > 0 {
			timeStr += " / " + formatDuration(p.cfg.Duration)
		}
	}

	title := p.cfg.Title
	maxTitleW := tw - len(timeStr) - len(qualityLabel) - 12
	if maxTitleW > 3 && len(title) > maxTitleW {
		title = title[:maxTitleW-3] + "..."
	}
	fmt.Fprintf(buf, " %s %s  \x1b[90m%s  %s\x1b[0m", icon, title, qualityLabel, timeStr)

	// Controls
	fmt.Fprintf(buf, "\x1b[%d;1H\x1b[2K \x1b[90m[space] pause  [←/→] seek  [m] mute  [r] loop  [1-4] quality  [/] search  [q] quit\x1b[0m", th)

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
	raw := make([]byte, 256)
	var carry []byte // leftover bytes from previous read (partial escape seq)

	for {
		n, err := p.tty.Read(raw)
		if err != nil || n == 0 {
			return
		}

		// Prepend any carry from a previous partial read
		var data []byte
		if len(carry) > 0 {
			data = append(carry, raw[:n]...)
			carry = nil
		} else {
			data = raw[:n]
		}

		for len(data) > 0 {
			b := data[0]

			if b == 0x1b {
				if len(data) < 3 {
					// Incomplete escape sequence, carry to next read
					carry = append([]byte{}, data...)
					data = nil
					break
				}

				if data[1] == '[' && data[2] == '<' {
					// SGR mouse event: find M or m terminator
					end := -1
					for i := 3; i < len(data); i++ {
						if data[i] == 'M' || data[i] == 'm' {
							end = i + 1
							break
						}
					}
					if end < 0 {
						// Incomplete mouse event, carry to next read
						carry = append([]byte{}, data...)
						data = nil
						break
					}
					p.handleMouse(data[:end])
					data = data[end:]
				} else if data[1] == '[' {
					// CSI sequence (arrow keys etc)
					switch data[2] {
					case 'C':
						p.seekRelative(5)
					case 'D':
						p.seekRelative(-5)
					case 'A':
						p.seekRelative(30)
					case 'B':
						p.seekRelative(-30)
					}
					// Skip to end of CSI: params are digits/semicolons, final byte is 0x40-0x7E
					i := 2
					for i < len(data) && ((data[i] >= '0' && data[i] <= '9') || data[i] == ';') {
						i++
					}
					if i < len(data) {
						i++ // consume final byte
					}
					data = data[i:]
				} else {
					// Other escape (bare ESC etc), skip 1 byte
					data = data[1:]
				}
				continue
			}

			// Single-byte key
			data = data[1:]
			if p.handleSingleKey(b) {
				return
			}
		}
	}
}

// handleSingleKey processes a single non-escape keypress. Returns true if the
// key handler should exit (quit or back-to-search).
func (p *player) handleSingleKey(b byte) bool {
	switch b {
	case 'q', 3:
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
		return true
	case ' ':
		p.togglePause()
	case 'l':
		p.seekRelative(10)
	case 'h':
		p.seekRelative(-10)
	case 'j':
		p.seekRelative(-60)
	case 'k':
		p.seekRelative(60)
	case 'm':
		p.muted.Store(!p.muted.Load())
	case 'r':
		p.looping.Store(!p.looping.Load())
	case '/':
		p.wantSearch.Store(true)
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
		return true
	case '1':
		p.requestQuality(360)
	case '2':
		p.requestQuality(480)
	case '3':
		p.requestQuality(720)
	case '4':
		p.requestQuality(1080)
	}
	return false
}

func (p *player) seekRelative(delta float64) {
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

	// Update position immediately so the bar jumps right away
	p.position = newPos
	p.audioBytesWritten.Store(0)

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

func (p *player) seekAbsolute(pos float64) {
	if pos < 0 {
		pos = 0
	}
	if p.cfg.Duration > 0 && pos > p.cfg.Duration {
		pos = p.cfg.Duration
	}

	// Update position immediately so the seek bar reflects the new spot
	// before ffmpeg restarts
	p.position = pos
	p.audioBytesWritten.Store(0)

	p.mu.Lock()
	if p.videoR != nil {
		p.videoR.Close()
		p.videoR = nil
	}
	p.mu.Unlock()

	select {
	case p.seekCh <- pos:
	default:
	}
}

func (p *player) handleMouse(data []byte) {
	s := string(data)
	idx := strings.Index(s, "\x1b[<")
	if idx < 0 {
		return
	}
	s = s[idx+3:]
	termIdx := strings.IndexAny(s, "Mm")
	if termIdx < 0 {
		return
	}
	isRelease := s[termIdx] == 'm'
	payload := s[:termIdx]

	var btn, col, row int
	if n, _ := fmt.Sscanf(payload, "%d;%d;%d", &btn, &col, &row); n != 3 {
		return
	}

	tw := int(p.tw.Load())
	th := int(p.th.Load())
	inBottomArea := row >= th-2

	// Release on video area: toggle pause (using release avoids focus-click issues)
	if btn == 0 && !inBottomArea && isRelease && !p.scrubbing.Load() {
		p.togglePause()
		return
	}

	// Everything below requires known duration and valid bar width
	if p.cfg.Duration <= 0 {
		return
	}
	barW := tw - 2
	if barW <= 0 {
		return
	}
	frac := max(0.0, min(1.0, float64(col-2)/float64(barW)))

	switch {
	// Scroll wheel: seek +-5s (ignore during scrub)
	case (btn == 64 || btn == 65) && !p.scrubbing.Load():
		if btn == 64 {
			p.seekRelative(5)
		} else {
			p.seekRelative(-5)
		}

	// Release: stop scrubbing, seek to final position
	case isRelease && p.scrubbing.Load():
		p.scrubbing.Store(false)
		p.seekAbsolute(frac * p.cfg.Duration)

	// Left click in bottom area: start scrub preview (seek happens on release)
	case btn == 0 && inBottomArea && !isRelease:
		p.scrubbing.Store(true)
		p.scrubFrac.Store(int64(frac * 10000))

	// Drag while scrubbing: update preview position
	case btn == 32 && p.scrubbing.Load():
		p.scrubFrac.Store(int64(frac * 10000))
	}
}

func (p *player) requestQuality(maxHeight int) {
	newW, newH := p.scaledSize(maxHeight)
	if newW == p.vw && newH == p.vh {
		return // already at this quality
	}

	// Get current position for seamless restart
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
	case p.qualityCh <- maxHeight:
	default:
	}
}

func (p *player) changeQuality(maxHeight int) error {
	// Calculate current playback position before stopping
	var currentPos float64
	if p.hasAudio.Load() {
		currentPos = p.position + p.audioPosSeconds()
	} else {
		currentPos = p.position
	}

	p.stopProcesses()
	p.cfg.MaxHeight = maxHeight

	// Show status in the controls row without clearing the frame
	th := int(p.th.Load())
	msg := fmt.Sprintf("Switching to %dp...", maxHeight)
	_, _ = syscall.Write(p.ttyFd, []byte(fmt.Sprintf("\x1b[%d;1H\x1b[2K \x1b[33m%s\x1b[0m", th, msg)))
	if err := p.resolveURLs(); err != nil {
		return fmt.Errorf("resolving URLs for %dp: %w", maxHeight, err)
	}

	p.vw, p.vh = p.scaledSize(maxHeight)
	p.frameSize = p.vw * p.vh * 3

	if err := p.setupMmap(); err != nil {
		return err
	}
	p.initFrameQueue()
	p.kittyHdr = fmt.Sprintf("\x1b_Ga=T,t=f,f=24,s=%d,v=%d,", p.vw, p.vh)
	p.qualityLabel = fmt.Sprintf("%dp", p.vh)

	p.dirty.Store(true)

	return p.startAt(currentPos)
}

func (p *player) togglePause() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.paused.Load() {
		for _, cmd := range []*exec.Cmd{p.ffmpegA, p.aplay} {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGCONT)
			}
		}
		p.paused.Store(false)
	} else {
		p.paused.Store(true)
		for _, cmd := range []*exec.Cmd{p.ffmpegA, p.aplay} {
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
		_, _ = p.tty.WriteString("\x1b[?1006l\x1b[?1002l\x1b_Ga=d;\x1b\\\x1b[?25h\x1b[?1049l")
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

// scaledSize computes decode dimensions preserving the source aspect ratio.
// Height is clamped to maxHeight; width is derived from the source ratio
// and rounded to even (ffmpeg requirement).
func (p *player) scaledSize(maxHeight int) (int, int) {
	sw, sh := p.srcW, p.srcH
	if sw <= 0 || sh <= 0 {
		sw, sh = 1920, 1080
	}

	h := min(sh, maxHeight)
	w := int(float64(sw) / float64(sh) * float64(h))
	// Round width to even for ffmpeg
	w = (w + 1) &^ 1
	h = (h + 1) &^ 1
	if w < 2 {
		w = 2
	}
	if h < 2 {
		h = 2
	}
	return w, h
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
