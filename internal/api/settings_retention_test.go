package api

import "testing"

func TestIsAllowedSettingKeyRetention(t *testing.T) {
	allowed := []string{
		"retention.max_hours.project.5",
		"retention.max_traces.project.42",
	}
	for _, k := range allowed {
		if !isAllowedSettingKey(k) {
			t.Errorf("%q should be an allowed setting key", k)
		}
	}
	denied := []string{
		"retention.max_hours",            // must be project-scoped
		"retention.max_traces",           // must be project-scoped
		"retention.evil.project.5",       // unknown retention subkey
		"retention_full_hours.project.5", // global key is not a project prefix
		"random.key",
	}
	for _, k := range denied {
		if isAllowedSettingKey(k) {
			t.Errorf("%q should NOT be an allowed setting key", k)
		}
	}
}
