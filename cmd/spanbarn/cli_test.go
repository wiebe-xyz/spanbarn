package main

import (
	"testing"
)

func TestSlugFromName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"My Project", "my-project"},
		{"  Spaced  Out  ", "spaced-out"},
		{"ALLCAPS", "allcaps"},
		{"special!@#chars", "specialchars"},
		{"dashes--already", "dashes-already"},
		{"trailing-", "trailing"},
		{"-leading", "leading"},
		{"hello world 123", "hello-world-123"},
		{"a  b  c", "a-b-c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugFromName(tt.name)
			if got != tt.want {
				t.Errorf("slugFromName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestPrintTable(t *testing.T) {
	// Just verify it doesn't panic with empty data.
	printTable([]string{}, nil)
	printTable([]string{"A", "B"}, nil)
	printTable([]string{"A", "B"}, [][]string{{"1", "2"}})
}
