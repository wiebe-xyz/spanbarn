package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- API shapes used by the TUI ---

type traceSummary struct {
	TraceID      string    `json:"traceId"`
	RootSpanName string    `json:"rootSpanName"`
	RootService  string    `json:"rootService"`
	DurationUs   int64     `json:"durationUs"`
	SpanCount    int       `json:"spanCount"`
	Status       string    `json:"status"`
	StartTime    time.Time `json:"startTime"`
}

type spanRow struct {
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Name         string `json:"name"`
	Service      string `json:"service"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	StartTimeUs  int64  `json:"startTimeUs"`
	DurationUs   int64  `json:"durationUs"`
}

type traceDetail struct {
	TraceID    string    `json:"traceId"`
	Spans      []spanRow `json:"spans"`
	DurationUs int64     `json:"durationUs"`
	Name       string    `json:"name"`
	TotalSpans int       `json:"totalSpans"`
	Truncated  bool      `json:"truncated"`
}

type logEntry struct {
	SeverityText string `json:"severityText"`
	TimeUnixNano int64  `json:"timeUnixNano"`
	Body         string `json:"body"`
	SpanID       string `json:"spanId"`
}

// --- model ---

type tuiView int

const (
	viewList tuiView = iota
	viewDetail
)

type tracesMsg struct {
	traces []traceSummary
	err    error
}

type detailMsg struct {
	detail traceDetail
	logs   []logEntry
	err    error
}

type tuiModel struct {
	client     *Client
	from, to   string
	errorsOnly bool
	service    string

	traces  []traceSummary
	cursor  int
	view    tuiView
	detail  traceDetail
	logs    []logEntry
	vp      viewport.Model
	width   int
	height  int
	loading bool
	err     error
}

func (m tuiModel) Init() tea.Cmd {
	return m.fetchTraces()
}

func (m tuiModel) fetchTraces() tea.Cmd {
	return func() tea.Msg {
		params := url.Values{}
		applyTimeRange(params, m.from, m.to)
		if m.errorsOnly {
			params.Set("status", "error")
		}
		if m.service != "" {
			params.Set("service", m.service)
		}
		params.Set("limit", "200")
		data, err := m.client.query("/api/v1/traces", params)
		if err != nil {
			return tracesMsg{err: err}
		}
		var traces []traceSummary
		if err := json.Unmarshal(data, &traces); err != nil {
			return tracesMsg{err: err}
		}
		// Slowest first — surfaces the worst flows.
		sort.SliceStable(traces, func(i, j int) bool {
			return traces[i].DurationUs > traces[j].DurationUs
		})
		return tracesMsg{traces: traces}
	}
}

func (m tuiModel) fetchDetail(traceID string) tea.Cmd {
	return func() tea.Msg {
		data, err := m.client.get("/api/v1/traces/" + url.PathEscape(traceID))
		if err != nil {
			return detailMsg{err: err}
		}
		var detail traceDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			return detailMsg{err: err}
		}

		// Correlated logs (best-effort; errors are non-fatal).
		var logs []logEntry
		lp := url.Values{}
		applyTimeRange(lp, m.from, m.to)
		lp.Set("trace_id", traceID)
		lp.Set("limit", "200")
		if ld, lerr := m.client.query("/api/v1/logs", lp); lerr == nil {
			var resp struct {
				Logs []logEntry `json:"logs"`
			}
			if json.Unmarshal(ld, &resp) == nil {
				logs = resp.Logs
			}
		}
		return detailMsg{detail: detail, logs: logs}
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.vp = viewport.New(msg.Width, max(1, msg.Height-3))
		if m.view == viewDetail {
			m.vp.SetContent(m.renderDetail())
		}
		return m, nil
	case tracesMsg:
		m.loading = false
		m.err = msg.err
		m.traces = msg.traces
		if m.cursor >= len(m.traces) {
			m.cursor = 0
		}
		return m, nil
	case detailMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.detail = msg.detail
		m.logs = msg.logs
		m.view = viewDetail
		m.vp.SetContent(m.renderDetail())
		m.vp.GotoTop()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	if m.view == viewDetail {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.view == viewDetail {
			m.view = viewList
			m.err = nil
			return m, nil
		}
		return m, tea.Quit
	}

	if m.view == viewDetail {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.traces)-1 {
			m.cursor++
		}
	case "enter":
		if len(m.traces) > 0 {
			m.loading = true
			return m, m.fetchDetail(m.traces[m.cursor].TraceID)
		}
	case "a":
		m.errorsOnly = !m.errorsOnly
		m.loading = true
		m.cursor = 0
		return m, m.fetchTraces()
	case "R":
		m.loading = true
		return m, m.fetchTraces()
	}
	return m, nil
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "Loading…"
	}
	if m.view == viewDetail {
		return m.viewDetail()
	}
	return m.viewList()
}

func (m tuiModel) viewList() string {
	mode := "all"
	if m.errorsOnly {
		mode = "errors"
	}
	header := headerStyle.Width(m.width).Render(
		fmt.Sprintf("SpanBarn traces — %s (%d)", mode, len(m.traces)))
	footer := footerStyle.Width(m.width).Render(
		helpItem("↑/↓", "navigate") + "  " +
			helpItem("enter", "drill in") + "  " +
			helpItem("a", "errors/all") + "  " +
			helpItem("R", "refresh") + "  " +
			helpItem("q", "quit"))

	body := m.listBody()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m tuiModel) listBody() string {
	if m.loading {
		return lipgloss.Place(m.width, max(1, m.height-2), lipgloss.Center, lipgloss.Center, "Loading…")
	}
	if m.err != nil {
		return lipgloss.Place(m.width, max(1, m.height-2), lipgloss.Center, lipgloss.Center,
			statusError.Render("Error: "+m.err.Error()))
	}
	if len(m.traces) == 0 {
		return lipgloss.Place(m.width, max(1, m.height-2), lipgloss.Center, lipgloss.Center,
			timeStyle.Render("No traces in range"))
	}

	maxVisible := max(1, m.height-2)
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	var rows []string
	for i := start; i < len(m.traces) && i < start+maxVisible; i++ {
		t := m.traces[i]
		icon := statusStyle(t.Status).Render(statusIcon(t.Status))
		name := truncate(t.RootSpanName, max(10, m.width-46))
		line := fmt.Sprintf("%s %-*s %s  %s  %s",
			icon, max(10, m.width-46), name,
			serviceStyle.Render(truncate(t.RootService, 18)),
			countStyle.Render(fmt.Sprintf("%9s", fmtDuration(t.DurationUs))),
			timeStyle.Render(fmt.Sprintf("%dsp %s", t.SpanCount, timeAgo(t.StartTime))))
		if i == m.cursor {
			rows = append(rows, selectedStyle.Width(m.width-2).Render(line))
		} else {
			rows = append(rows, normalStyle.Render(line))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m tuiModel) viewDetail() string {
	title := truncate(m.detail.Name, max(10, m.width-4))
	header := headerStyle.Width(m.width).Render(title)
	footer := footerStyle.Width(m.width).Render(
		helpItem("esc", "back") + "  " + helpItem("↑/↓", "scroll") + "  " + helpItem("q", "quit"))
	m.vp.Width = m.width
	m.vp.Height = max(1, m.height-2)
	return lipgloss.JoinVertical(lipgloss.Left, header, m.vp.View(), footer)
}

func (m tuiModel) renderDetail() string {
	d := m.detail
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(titleColor).Render("Trace") + "\n")
	b.WriteString(fmt.Sprintf("  ID:       %s\n", d.TraceID))
	b.WriteString(fmt.Sprintf("  Duration: %s\n", fmtDuration(d.DurationUs)))
	b.WriteString(fmt.Sprintf("  Spans:    %d\n", d.TotalSpans))
	if d.Truncated {
		b.WriteString(timeStyle.Render(fmt.Sprintf("  (showing first %d spans)\n", len(d.Spans))))
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(titleColor).Render("Spans") + "\n")
	for _, line := range spanTree(d.Spans) {
		b.WriteString("  " + line + "\n")
	}

	if len(m.logs) > 0 {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(titleColor).Render("Correlated logs") + "\n")
		for _, l := range m.logs {
			sev := strings.ToUpper(l.SeverityText)
			st := timeStyle
			if sev == "ERROR" || sev == "FATAL" {
				st = statusError
			}
			ts := time.Unix(0, l.TimeUnixNano).UTC().Format("15:04:05")
			b.WriteString(fmt.Sprintf("  %s %s %s\n",
				timeStyle.Render(ts), st.Render(fmt.Sprintf("%-5s", sev)), truncate(l.Body, max(20, m.width-20))))
		}
	}
	return b.String()
}

// spanTree renders parent→child indented lines ordered by start time.
func spanTree(spans []spanRow) []string {
	children := map[string][]spanRow{}
	hasParent := map[string]bool{}
	byID := map[string]bool{}
	for _, s := range spans {
		byID[s.SpanID] = true
	}
	for _, s := range spans {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
		if s.ParentSpanID != "" && byID[s.ParentSpanID] {
			hasParent[s.SpanID] = true
		}
	}
	for k := range children {
		sort.SliceStable(children[k], func(i, j int) bool {
			return children[k][i].StartTimeUs < children[k][j].StartTimeUs
		})
	}

	var lines []string
	var walk func(s spanRow, depth int)
	walk = func(s spanRow, depth int) {
		indent := strings.Repeat("  ", depth)
		icon := statusStyle(s.Status).Render(statusIcon(s.Status))
		line := fmt.Sprintf("%s%s %s %s %s",
			indent, icon, s.Name,
			serviceStyle.Render(s.Service),
			countStyle.Render(fmtDuration(s.DurationUs)))
		lines = append(lines, line)
		for _, c := range children[s.SpanID] {
			walk(c, depth+1)
		}
	}
	// Roots: spans whose parent is absent from the set.
	for _, s := range spans {
		if !hasParent[s.SpanID] {
			walk(s, 0)
		}
	}
	return lines
}

func cmdTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	all := fs.Bool("all", false, "start showing all traces (default: errors only)")
	service := fs.String("service", "", "filter by service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	m := tuiModel{
		client:     client,
		from:       *from,
		to:         *to,
		errorsOnly: !*all,
		service:    *service,
		loading:    true,
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// --- helpers ---

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func fmtDuration(us int64) string {
	switch {
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.2fs", float64(us)/1_000_000)
	}
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
