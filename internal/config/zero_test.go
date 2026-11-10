package config

import "testing"

// Four settings are documented as off at 0. The environment parser used to read
// 0 as unset, so an operator who set 0 got the default back and could not
// switch the feature off.
func TestZeroSwitchesOffDocumentedSettings(t *testing.T) {
	cases := []struct {
		key      string
		def      int
		read     func(Config) int
		describe string
	}{
		{"SPANBARN_DB_BALLAST_MB", 256, func(c Config) int { return c.Retention.BallastMB }, "0 disables the reserve"},
		{"SPANBARN_INGEST_REJECT_DISK_PCT", 95, func(c Config) int { return c.IngestRejectDiskPct }, "0 disables shedding"},
		{"SPANBARN_READ_BUDGET_SECONDS", 10, func(c Config) int { return c.ReadBudgetSeconds }, "0 turns the budget off"},
		{"SPANBARN_TRACE_BUFFER_MAX_SPANS", 50000, func(c Config) int { return c.TraceBufferMaxSpans }, "0 disables the cap"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := tc.read(Load()); got != tc.def {
				t.Fatalf("default = %d, want %d", got, tc.def)
			}
			t.Setenv(tc.key, "0")
			if got := tc.read(Load()); got != 0 {
				t.Errorf("%s: %s, but 0 loaded %d", tc.key, tc.describe, got)
			}
			t.Setenv(tc.key, "7")
			if got := tc.read(Load()); got != 7 {
				t.Errorf("explicit 7 loaded %d", got)
			}
			for _, bad := range []string{"-3", "x"} {
				t.Setenv(tc.key, bad)
				if got := tc.read(Load()); got != tc.def {
					t.Errorf("%q loaded %d, want the default %d", bad, got, tc.def)
				}
			}
		})
	}
}

// Only the exact string "off" used to turn span rollup reads off. The sibling
// switches accept 0, false, no and off in any case.
func TestSpanRollupReadsSwitch(t *testing.T) {
	const key = "SPANBARN_SPAN_ROLLUP_READS"
	for raw, want := range map[string]bool{
		"": true, "auto": true, "on": true, "1": true, "true": true,
		"off": false, "OFF": false, "0": false, "false": false, "no": false,
	} {
		t.Setenv(key, raw)
		if got := Load().SpanRollupReads; got != want {
			t.Errorf("%s=%q: SpanRollupReads = %v, want %v", key, raw, got, want)
		}
	}
}

// Boring retention cannot be switched off. The span classifier stamps
// expires_at = ingested + this value, so 0 would expire every boring span at
// once. Load and the retention worker both replace 0 with the default.
func TestBoringRetentionZeroKeepsDefault(t *testing.T) {
	t.Setenv("SPANBARN_BORING_RETENTION_MINUTES", "0")
	if got := Load().Retention.BoringMinutes; got != 30 {
		t.Fatalf("BoringMinutes = %d, want the default 30", got)
	}
}
