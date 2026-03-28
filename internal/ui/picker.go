package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Gaurav-Gosain/youterm/internal/ytdlp"
)

type model struct {
	results []ytdlp.Result
	cursor  int
	chosen  *ytdlp.Result
	width   int
	height  int
	offset  int // scroll offset for long lists
}

func Pick(results []ytdlp.Result) (*ytdlp.Result, error) {
	m := model{results: results, width: 80, height: 24}
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	return final.(model).chosen, nil
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.ensureVisible()
			}
		case "down", "j":
			if m.cursor < len(m.results)-1 {
				m.cursor++
				m.ensureVisible()
			}
		case "home", "g":
			m.cursor = 0
			m.ensureVisible()
		case "end", "G":
			m.cursor = len(m.results) - 1
			m.ensureVisible()
		case "enter":
			m.chosen = &m.results[m.cursor]
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *model) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	} else if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
}

func (m model) visibleRows() int {
	// header (3 lines) + footer (2 lines) = 5 lines overhead
	// each result takes 3 lines (title + meta + blank)
	rows := max((m.height-5)/3, 1)
	return rows
}

func (m model) View() tea.View {
	var b strings.Builder

	fmt.Fprintf(&b, "\n  \033[1mSearch Results\033[0m (%d)\n\n", len(m.results))

	visible := m.visibleRows()
	end := min(m.offset+visible, len(m.results))

	for i := m.offset; i < end; i++ {
		r := m.results[i]

		title := r.Title
		maxW := m.width - 6
		if maxW > 10 && len(title) > maxW {
			title = title[:maxW-3] + "..."
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

		if i == m.cursor {
			fmt.Fprintf(&b, "  \033[1;7m > %s \033[0m\n", title)
			fmt.Fprintf(&b, "    \033[1;2m%s\033[0m\n\n", meta)
		} else {
			fmt.Fprintf(&b, "    %s\n", title)
			fmt.Fprintf(&b, "    \033[2m%s\033[0m\n\n", meta)
		}
	}

	if m.offset+visible < len(m.results) {
		b.WriteString("    \033[2m...\033[0m\n")
	}

	b.WriteString("\n  \033[2m[enter] play  [j/k] navigate  [q] quit\033[0m")

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}
