package worker

import "testing"

// mapSettingsReader is a minimal in-memory BoringSettingsReader for tests.
type mapSettingsReader map[string]string

func (m mapSettingsReader) GetSetting(key string) (string, error) { return m[key], nil }

func TestMinTracesPerMinuteDefault(t *testing.T) {
	p := NewCachedBoringPolicy(mapSettingsReader{}, 0)
	if got := p.MinTracesPerMinute(7); got != DefaultMinTracesPerMinute {
		t.Fatalf("default: want %d, got %d", DefaultMinTracesPerMinute, got)
	}
}

func TestMinTracesPerMinuteGlobal(t *testing.T) {
	p := NewCachedBoringPolicy(mapSettingsReader{"boring.min_traces_per_minute": "5"}, 0)
	if got := p.MinTracesPerMinute(7); got != 5 {
		t.Fatalf("global: want 5, got %d", got)
	}
}

func TestMinTracesPerMinuteProjectOverridesGlobal(t *testing.T) {
	p := NewCachedBoringPolicy(mapSettingsReader{
		"boring.min_traces_per_minute":           "5",
		"boring.min_traces_per_minute.project.7": "2",
	}, 0)
	if got := p.MinTracesPerMinute(7); got != 2 {
		t.Fatalf("project override: want 2, got %d", got)
	}
	// A different project falls back to the global value.
	if got := p.MinTracesPerMinute(9); got != 5 {
		t.Fatalf("fallback to global: want 5, got %d", got)
	}
}

func TestMinTracesPerMinuteNegativeClampedToDefault(t *testing.T) {
	// A negative value is invalid (n >= 0 guard fails) → falls through to default.
	p := NewCachedBoringPolicy(mapSettingsReader{"boring.min_traces_per_minute": "-3"}, 0)
	if got := p.MinTracesPerMinute(7); got != DefaultMinTracesPerMinute {
		t.Fatalf("negative: want default %d, got %d", DefaultMinTracesPerMinute, got)
	}
}

func TestMinTracesPerMinuteZeroAllowed(t *testing.T) {
	// 0 is a valid explicit value (disable the floor for this project).
	p := NewCachedBoringPolicy(mapSettingsReader{"boring.min_traces_per_minute.project.7": "0"}, 0)
	if got := p.MinTracesPerMinute(7); got != 0 {
		t.Fatalf("explicit zero: want 0, got %d", got)
	}
}
