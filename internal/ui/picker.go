package ui

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/term"

	"github.com/Gaurav-Gosain/youterm/internal/ytdlp"
)

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func getWinsize(fd int) winsize {
	var ws winsize
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	return ws
}

const (
	thumbCols    = 20
	thumbRows    = 5
	rowsPerEntry = 6
	headerRows   = 4 // search bar + result count + blank
	footerRows   = 2
)

type pickerState struct {
	tty *os.File
	fd  int

	results    []ytdlp.Result
	thumbs     []*thumb
	mu         sync.Mutex
	thumbDone  chan struct{}
	searchCount int

	cursor  int
	offset  int
	input   []byte   // search bar text
	editing bool     // true when search bar is focused
	loading bool     // true while searching

	redrawCh chan struct{}
}

func (ps *pickerState) redraw() {
	select {
	case ps.redrawCh <- struct{}{}:
	default:
	}
}

func (ps *pickerState) startThumbDownloads() {
	ps.mu.Lock()
	for _, t := range ps.thumbs {
		if t != nil {
			t.cleanup()
		}
	}
	ps.thumbs = make([]*thumb, len(ps.results))
	ps.mu.Unlock()

	done := make(chan struct{}, len(ps.results))
	ps.thumbDone = done

	var wg sync.WaitGroup
	for i, r := range ps.results {
		wg.Add(1)
		go func(idx int, videoID string) {
			defer wg.Done()
			t, err := downloadThumb(videoID)
			if err == nil {
				ps.mu.Lock()
				ps.thumbs[idx] = t
				ps.mu.Unlock()
				done <- struct{}{}
			}
		}(i, r.ID)
	}
	// Close channel when all downloads finish so the forwarder exits
	go func() { wg.Wait(); close(done) }()

	go func() {
		for range done {
			ps.redraw()
		}
	}()
}

func (ps *pickerState) doSearch() {
	query := strings.TrimSpace(string(ps.input))
	if query == "" {
		return
	}

	ps.loading = true
	ps.editing = false
	ps.redraw()

	results, err := ytdlp.Search(query, ps.searchCount)

	ps.loading = false
	if err != nil || len(results) == 0 {
		ps.results = nil
		ps.thumbs = nil
		ps.cursor = 0
		ps.offset = 0
		ps.editing = true // go back to search bar
		ps.redraw()
		return
	}

	ps.results = results
	ps.cursor = 0
	ps.offset = 0
	ps.startThumbDownloads()
	ps.redraw()
}

// Pick presents an interactive picker with an integrated search bar.
// If initialResults is non-empty, it shows those results immediately.
// Otherwise it starts in search mode. searchCount controls how many
// results yt-dlp returns.
func Pick(initialResults []ytdlp.Result, searchCount int) (*ytdlp.Result, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer tty.Close()

	fd := int(tty.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, oldState)

	_, _ = tty.WriteString("\x1b[?1049h\x1b[?25l")
	defer func() {
		_, _ = tty.WriteString("\x1b_Ga=d;\x1b\\\x1b[?25h\x1b[?1049l")
	}()

	ps := &pickerState{
		tty:         tty,
		fd:          fd,
		results:     initialResults,
		searchCount: searchCount,
		editing:     len(initialResults) == 0,
		redrawCh:    make(chan struct{}, 1),
	}

	// Cleanup thumbs on exit
	defer func() {
		ps.mu.Lock()
		for _, t := range ps.thumbs {
			if t != nil {
				t.cleanup()
			}
		}
		ps.mu.Unlock()
	}()

	if len(initialResults) > 0 {
		ps.startThumbDownloads()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer func() { signal.Stop(sigCh); close(sigCh) }()
	go func() {
		for range sigCh {
			ps.redraw()
		}
	}()

	// Key input
	keyCh := make(chan []byte, 4)
	stopKeys := make(chan struct{})
	go func() {
		buf := make([]byte, 16)
		for {
			n, err := tty.Read(buf)
			if err != nil || n == 0 {
				return
			}
			b := make([]byte, n)
			copy(b, buf[:n])
			select {
			case keyCh <- b:
			case <-stopKeys:
				return
			}
		}
	}()
	defer close(stopKeys)

	ps.redraw()

	for {
		select {
		case <-ps.redrawCh:
			drawPicker(ps)
		case key := <-keyCh:
			if len(key) == 0 {
				continue
			}

			if ps.editing {
				result := handleSearchInput(ps, key)
				if result != nil {
					return result, nil
				}
			} else {
				result, done := handleResultsInput(ps, key)
				if done {
					return result, nil
				}
			}
		}
	}
}

// handleSearchInput handles keys when the search bar is focused.
// Returns non-nil result only on quit (returns nil, signaling abort).
func handleSearchInput(ps *pickerState, key []byte) *ytdlp.Result {
	switch {
	case key[0] == 3: // ctrl-c
		return &ytdlp.Result{} // sentinel for quit (checked below)
	case key[0] == 0x1b:
		if len(key) == 1 {
			// Esc: if we have results, go back to result list
			if len(ps.results) > 0 {
				ps.editing = false
				ps.redraw()
				return nil
			}
			return &ytdlp.Result{} // quit
		}
		// Ignore escape sequences in edit mode
		return nil
	case key[0] == '\r' || key[0] == '\n':
		go ps.doSearch()
		return nil
	case key[0] == 127 || key[0] == 8: // backspace
		if len(ps.input) > 0 {
			ps.input = ps.input[:len(ps.input)-1]
			ps.redraw()
		}
		return nil
	case key[0] == 21: // ctrl-u: clear input
		ps.input = nil
		ps.redraw()
		return nil
	case key[0] >= 32 && key[0] < 127: // printable ASCII
		ps.input = append(ps.input, key[0])
		ps.redraw()
		return nil
	default:
		// UTF-8 multibyte — append all bytes
		if key[0] >= 0xC0 {
			ps.input = append(ps.input, key...)
			ps.redraw()
		}
		return nil
	}
}

// handleResultsInput handles keys when browsing results.
func handleResultsInput(ps *pickerState, key []byte) (*ytdlp.Result, bool) {
	ws := getWinsize(ps.fd)
	visible := visibleEntries(int(ws.Row))

	// Arrow keys
	if len(key) >= 3 && key[0] == 0x1b && key[1] == '[' {
		switch key[2] {
		case 'A':
			if ps.cursor > 0 {
				ps.cursor--
				ensureVisible(ps.cursor, &ps.offset, visible)
				ps.redraw()
			}
		case 'B':
			if ps.cursor < len(ps.results)-1 {
				ps.cursor++
				ensureVisible(ps.cursor, &ps.offset, visible)
				ps.redraw()
			}
		}
		return nil, false
	}

	switch key[0] {
	case 'q', 3: // q or ctrl-c
		return nil, true
	case 0x1b: // bare esc
		return nil, true
	case '\r', '\n':
		if len(ps.results) > 0 {
			return &ps.results[ps.cursor], true
		}
	case '/':
		ps.editing = true
		ps.redraw()
	case 'j':
		if ps.cursor < len(ps.results)-1 {
			ps.cursor++
			ensureVisible(ps.cursor, &ps.offset, visible)
			ps.redraw()
		}
	case 'k':
		if ps.cursor > 0 {
			ps.cursor--
			ensureVisible(ps.cursor, &ps.offset, visible)
			ps.redraw()
		}
	case 'g':
		ps.cursor = 0
		ps.offset = 0
		ps.redraw()
	case 'G':
		if len(ps.results) > 0 {
			ps.cursor = len(ps.results) - 1
			ps.offset = max(0, ps.cursor-visible+1)
			ps.redraw()
		}
	}
	return nil, false
}

func visibleEntries(termRows int) int {
	return max((termRows-headerRows-footerRows)/rowsPerEntry, 1)
}

func ensureVisible(cursor int, offset *int, visible int) {
	if cursor < *offset {
		*offset = cursor
	} else if cursor >= *offset+visible {
		*offset = cursor - visible + 1
	}
}

func drawPicker(ps *pickerState) {
	ws := getWinsize(ps.fd)
	tw, th := int(ws.Col), int(ws.Row)
	if tw == 0 {
		tw = 80
	}
	if th == 0 {
		th = 24
	}

	visible := visibleEntries(th)
	if ps.cursor < ps.offset {
		ps.offset = ps.cursor
	} else if ps.cursor >= ps.offset+visible {
		ps.offset = ps.cursor - visible + 1
	}
	offset := ps.offset

	var buf bytes.Buffer

	buf.WriteString("\x1b[?2026h")
	buf.WriteString("\x1b[2J\x1b[H")

	// Search bar
	searchW := tw - 6
	if searchW < 10 {
		searchW = 10
	}
	inputStr := string(ps.input)
	displayInput := inputStr
	if len(displayInput) > searchW-2 {
		displayInput = displayInput[len(displayInput)-searchW+2:]
	}

	if ps.editing {
		// Show cursor in search bar
		fmt.Fprintf(&buf, "\x1b[1;3H\x1b[1m\uf002 \x1b[0m\x1b[4m%-*s\x1b[0m", searchW, displayInput+"_")
	} else {
		if len(inputStr) > 0 {
			fmt.Fprintf(&buf, "\x1b[1;3H\x1b[2m\uf002 %s\x1b[0m", displayInput)
		} else {
			fmt.Fprintf(&buf, "\x1b[1;3H\x1b[2m\uf002 Search YouTube...\x1b[0m")
		}
	}

	if ps.loading {
		fmt.Fprintf(&buf, "\x1b[2;3H\x1b[2mSearching...\x1b[0m")
	} else if len(ps.results) > 0 {
		fmt.Fprintf(&buf, "\x1b[2;3H\x1b[2m%d results\x1b[0m", len(ps.results))
	} else if len(ps.input) > 0 && !ps.editing {
		fmt.Fprintf(&buf, "\x1b[2;3H\x1b[2mNo results found.\x1b[0m")
	}

	// Results
	if len(ps.results) > 0 {
		end := min(offset+visible, len(ps.results))

		textCol := 3 + thumbCols + 2
		maxTextW := tw - textCol - 2
		if maxTextW < 10 {
			maxTextW = 10
		}

		for i := 0; i < visible && offset+i < len(ps.results); i++ {
			idx := offset + i
			r := ps.results[idx]
			row := headerRows + 1 + i*rowsPerEntry

			ps.mu.Lock()
			var t *thumb
			if idx < len(ps.thumbs) {
				t = ps.thumbs[idx]
			}
			ps.mu.Unlock()

			if t != nil {
				fmt.Fprintf(&buf, "\x1b[%d;3H%s", row, t.displayCmd(thumbCols, thumbRows))
			} else {
				drawPlaceholder(&buf, row, 3)
			}

			if idx == ps.cursor && !ps.editing {
				for rr := 0; rr < thumbRows; rr++ {
					fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[1;36m\u2590\x1b[0m", row+rr)
				}
			}

			title := r.Title
			if len(title) > maxTextW {
				title = title[:maxTextW-3] + "..."
			}

			dur := ytdlp.FormatDuration(r.Duration)
			views := ytdlp.FormatViews(r.ViewCount)
			channel := r.Channel
			if len(channel) > 30 {
				channel = channel[:27] + "..."
			}
			meta := dur
			if channel != "" {
				meta += "  " + channel
			}
			if views != "" {
				meta += "  " + views
			}
			if len(meta) > maxTextW {
				meta = meta[:maxTextW-3] + "..."
			}

			if idx == ps.cursor && !ps.editing {
				fmt.Fprintf(&buf, "\x1b[%d;%dH\x1b[1;36m%s\x1b[0m", row+1, textCol, title)
			} else {
				fmt.Fprintf(&buf, "\x1b[%d;%dH\x1b[1m%s\x1b[0m", row+1, textCol, title)
			}
			fmt.Fprintf(&buf, "\x1b[%d;%dH\x1b[2m%s\x1b[0m", row+2, textCol, meta)
		}

		if end < len(ps.results) {
			fmt.Fprintf(&buf, "\x1b[%d;3H\x1b[2m... %d more\x1b[0m", th-2, len(ps.results)-end)
		}
	}

	// Footer
	if ps.editing {
		fmt.Fprintf(&buf, "\x1b[%d;3H\x1b[2m[enter] search  [esc] back  [ctrl+c] quit\x1b[0m", th-1)
	} else {
		fmt.Fprintf(&buf, "\x1b[%d;3H\x1b[2m[j/k] navigate  [enter] play  [/] search  [q] quit\x1b[0m", th-1)
	}

	buf.WriteString("\x1b[?2026l")
	_, _ = ps.tty.Write(buf.Bytes())
}

func drawPlaceholder(buf *bytes.Buffer, row, col int) {
	top := "\u256d" + strings.Repeat("\u2500", thumbCols-2) + "\u256e"
	bot := "\u2570" + strings.Repeat("\u2500", thumbCols-2) + "\u256f"
	mid := "\u2502" + strings.Repeat(" ", thumbCols-2) + "\u2502"
	fmt.Fprintf(buf, "\x1b[%d;%dH\x1b[90m%s\x1b[0m", row, col, top)
	for r := 1; r < thumbRows-1; r++ {
		fmt.Fprintf(buf, "\x1b[%d;%dH\x1b[90m%s\x1b[0m", row+r, col, mid)
	}
	fmt.Fprintf(buf, "\x1b[%d;%dH\x1b[90m%s\x1b[0m", row+thumbRows-1, col, bot)
	label := "..."
	fmt.Fprintf(buf, "\x1b[%d;%dH\x1b[90m%s\x1b[0m", row+thumbRows/2, col+(thumbCols-len(label))/2, label)
}
