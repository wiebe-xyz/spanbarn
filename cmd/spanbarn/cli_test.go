package main

import (
	"encoding/hex"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	key, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey() error: %v", err)
	}
	if len(key) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(key))
	}
	// Verify it's valid hex.
	if _, err := hex.DecodeString(key); err != nil {
		t.Errorf("key is not valid hex: %v", err)
	}

	// Verify uniqueness (two calls produce different keys).
	key2, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey() second call error: %v", err)
	}
	if key == key2 {
		t.Error("two generated keys should not be identical")
	}
}

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
