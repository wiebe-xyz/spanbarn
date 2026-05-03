package aggregation

import (
	"testing"
	"time"
)

func TestTruncateToBucket1Min(t *testing.T) {
	cases := []struct {
		in   time.Time
		want time.Time
	}{
		{
			in:   time.Date(2026, 5, 3, 10, 23, 45, 0, time.UTC),
			want: time.Date(2026, 5, 3, 10, 23, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
			want: time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 5, 3, 10, 59, 59, 999_999_999, time.UTC),
			want: time.Date(2026, 5, 3, 10, 59, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		got := TruncateToBucket(tc.in, time.Minute)
		if !got.Equal(tc.want) {
			t.Errorf("TruncateToBucket(%v, 1m) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTruncateToBucket5Min(t *testing.T) {
	cases := []struct {
		in   time.Time
		want time.Time
	}{
		{
			in:   time.Date(2026, 5, 3, 10, 7, 30, 0, time.UTC),
			want: time.Date(2026, 5, 3, 10, 5, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 5, 3, 10, 14, 59, 0, time.UTC),
			want: time.Date(2026, 5, 3, 10, 10, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		got := TruncateToBucket(tc.in, 5*time.Minute)
		if !got.Equal(tc.want) {
			t.Errorf("TruncateToBucket(%v, 5m) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTruncateToBucket1Hour(t *testing.T) {
	in := time.Date(2026, 5, 3, 10, 45, 30, 0, time.UTC)
	want := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	got := TruncateToBucket(in, time.Hour)
	if !got.Equal(want) {
		t.Errorf("TruncateToBucket(%v, 1h) = %v, want %v", in, got, want)
	}
}

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"1m", time.Minute},
		{"5m", 5 * time.Minute},
		{"15m", 15 * time.Minute},
		{"1h", time.Hour},
	}
	for _, tc := range cases {
		got, err := ParseInterval(tc.in)
		if err != nil {
			t.Errorf("ParseInterval(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseInterval(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseIntervalInvalid(t *testing.T) {
	for _, s := range []string{"", "2m", "10m", "30m", "2h", "abc", "1s"} {
		_, err := ParseInterval(s)
		if err == nil {
			t.Errorf("ParseInterval(%q) expected error, got nil", s)
		}
	}
}
