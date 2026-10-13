package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"path/filepath"
	"sort"
	"strings"
)

type picker struct {
	root     string
	rows     []Repo
	selected map[string]bool
	visible  []int
	cursor   int
	filter   string
	typing   bool
	details  bool
	sizeSort bool
	height   int
	action   string
	chosen   []string
}

func newPicker(rows []Repo) picker {
	p := picker{rows: append([]Repo(nil), rows...), selected: map[string]bool{}, height: 24}
	if len(rows) > 0 {
		p.root = filepath.Dir(rows[0].Path)
		for _, r := range rows {
			for !inside(p.root, r.Path) && filepath.Dir(p.root) != p.root {
				p.root = filepath.Dir(p.root)
			}
		}
	}
	p.rebuild()
	return p
}
func (p *picker) rebuild() {
	sort.Slice(p.rows, func(i, j int) bool {
		if p.sizeSort && p.rows[i].AllocatedBytes != p.rows[j].AllocatedBytes {
			return p.rows[i].AllocatedBytes > p.rows[j].AllocatedBytes
		}
		if !p.sizeSort && p.rows[i].Status != p.rows[j].Status {
			return p.rows[i].Status < p.rows[j].Status
		}
		return p.rows[i].Path < p.rows[j].Path
	})
	p.visible = nil
	for i, r := range p.rows {
		if strings.Contains(strings.ToLower(r.Path+" "+r.Origin+" "+r.Status), strings.ToLower(p.filter)) {
			p.visible = append(p.visible, i)
		}
	}
	if p.cursor >= len(p.visible) {
		p.cursor = len(p.visible) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}
func (p picker) Init() tea.Cmd { return nil }
func (p picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		p.height = m.Height
	case tea.KeyMsg:
		if p.typing {
			switch m.String() {
			case "enter", "esc":
				p.typing = false
			case "backspace", "ctrl+h":
				r := []rune(p.filter)
				if len(r) > 0 {
					p.filter = string(r[:len(r)-1])
				}
			default:
				if len(m.Runes) > 0 {
					p.filter += string(m.Runes)
				}
			}
			p.rebuild()
			return p, nil
		}
		switch m.String() {
		case "q", "ctrl+c":
			return p, tea.Quit
		case "/":
			p.typing = true
		case "esc":
			p.details = false
			p.filter = ""
			p.rebuild()
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor+1 < len(p.visible) {
				p.cursor++
			}
		case "pgdown":
			p.cursor += 10
			if p.cursor >= len(p.visible) {
				p.cursor = len(p.visible) - 1
			}
			if p.cursor < 0 {
				p.cursor = 0
			}
		case "pgup":
			p.cursor -= 10
			if p.cursor < 0 {
				p.cursor = 0
			}
		case " ":
			if len(p.visible) > 0 {
				s := p.rows[p.visible[p.cursor]].Path
				p.selected[s] = !p.selected[s]
			}
		case "a":
			for _, i := range p.visible {
				p.selected[p.rows[i].Path] = true
			}
		case "x":
			p.selected = map[string]bool{}
		case "s":
			p.sizeSort = !p.sizeSort
			p.rebuild()
		case "d", "enter":
			p.details = !p.details
		case "p", "c", "g", "o", "t":
			for s, chosen := range p.selected {
				if chosen {
					p.chosen = append(p.chosen, s)
				}
			}
			if len(p.chosen) == 0 && len(p.visible) > 0 {
				p.chosen = []string{p.rows[p.visible[p.cursor]].Path}
			}
			if len(p.chosen) > 0 {
				p.action = map[string]string{"p": "push", "c": "commit-push", "g": "create-origin", "o": "open", "t": "trash"}[m.String()]
				return p, tea.Quit
			}
		}
	}
	return p, nil
}
func (p picker) View() string {
	var b strings.Builder
	count := 0
	for _, v := range p.selected {
		if v {
			count++
		}
	}
	fmt.Fprintf(&b, "git-shed · %d repositories · %d selected\nFilter: %s\n", len(p.rows), count, p.filter)
	if p.typing {
		fmt.Fprintln(&b, "Type filter; Enter applies it.")
	}
	fmt.Fprint(&b, "↑↓ move · space select · / filter · s sort · d details · q quit\na select filtered · x clear · p push · c commit/push\ng create origin · o browser · t Trash\n\n")
	if p.details && len(p.visible) > 0 {
		r := p.rows[p.visible[p.cursor]]
		fmt.Fprintf(&b, "%s\nOrigin: %s\nStatus: %s (%s)\nHEAD: %s\nAllocated: %s\nBlockers: %s\n", r.Path, redact(r.Origin), r.Status, r.Verification, r.Head, humanSize(r.AllocatedBytes), strings.Join(r.Blockers, "; "))
		remaining := p.height - 15
		for _, group := range []struct {
			name  string
			paths []string
		}{{"Ignored", r.Ignored}, {"Untracked", r.Untracked}, {"Nested", r.Nested}} {
			fmt.Fprintf(&b, "%s (%d):\n", group.name, len(group.paths))
			for _, s := range group.paths {
				if remaining <= 0 {
					fmt.Fprintln(&b, "  … use scan --format json for all paths")
					break
				}
				fmt.Fprintln(&b, "  "+s)
				remaining--
			}
		}
		return b.String()
	}
	n := p.height - 8
	if n < 1 {
		n = 1
	}
	start := 0
	if p.cursor >= n {
		start = p.cursor - n + 1
	}
	for pos := start; pos < len(p.visible) && pos < start+n; pos++ {
		r := p.rows[p.visible[pos]]
		cursor := " "
		if pos == p.cursor {
			cursor = ">"
		}
		mark := " "
		if p.selected[r.Path] {
			mark = "x"
		}
		fmt.Fprintf(&b, "%s[%s] %-17s %9s %s\n", cursor, mark, r.Status, humanSize(r.AllocatedBytes), displayPath(p.root, r.Path))
	}
	return b.String()
}
func pick(rows []Repo) (string, []string, error) {
	m, e := tea.NewProgram(newPicker(rows), tea.WithAltScreen()).Run()
	if e != nil {
		return "", nil, e
	}
	p := m.(picker)
	return p.action, p.chosen, nil
}
func displayPath(root, path string) string {
	if root != "" {
		if rel, e := filepath.Rel(root, path); e == nil {
			return rel
		}
	}
	return path
}
