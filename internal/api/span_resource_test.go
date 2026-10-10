package api

import (
	"encoding/json"
	"testing"
)

func TestExtractResource(t *testing.T) {
	cases := []struct {
		name  string
		span  string
		attrs map[string]any
		want  string
	}{
		{"http route wins", "claude_code.tool", map[string]any{"http.route": "/x", "tool_name": "Bash"}, "/x"},
		{"tool span uses tool_name", "claude_code.tool", map[string]any{"tool_name": "Bash"}, "Bash"},
		{"tool execution uses tool_name", "claude_code.tool.execution", map[string]any{"tool_name": "Read"}, "Read"},
		{"blocked on user uses tool_name", "claude_code.tool.blocked_on_user", map[string]any{"tool_name": "Edit"}, "Edit"},
		{"llm request uses model", "claude_code.llm_request", map[string]any{"model": "claude-opus-4-7"}, "claude-opus-4-7"},
		{"interaction has none", "claude_code.interaction", map[string]any{"tool_name": "Bash", "model": "m"}, ""},
		{"other span ignores tool_name", "HTTP GET", map[string]any{"tool_name": "Bash"}, ""},
		{"empty tool_name", "claude_code.tool", map[string]any{"tool_name": ""}, ""},
	}
	for _, c := range cases {
		raw, _ := json.Marshal(c.attrs)
		if got := extractResource(c.span, raw); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
