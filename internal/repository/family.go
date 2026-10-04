package repository

// Family names a group of tables that can live in their own database file.
// Every write transaction touches the tables of one family only, because
// SQLite commits each attached WAL database separately: a transaction that
// spans two files is atomic per file, and a crash between the two commits
// leaves them disagreeing. See specs/002-temporal-sharding/design.md.
//
// In a single-file layout every family resolves to the same handle and the
// family only decides which write scheduler a job queues on.
type Family int

const (
	// FamilyCore holds configuration and everything not listed below:
	// projects, users, keys, alerts, SLOs, boards, rollups, settings.
	FamilyCore Family = iota
	// FamilySpans holds the span pipeline's tables. They are written together
	// (aggregate-then-delete, error-sample copy, trace structure refresh), so
	// they share one file.
	FamilySpans
	FamilyLogs
	FamilyMetrics
	FamilyPrompts
	numFamilies
)

// familyTables lists the tables of every family except core. A table not
// listed here belongs to FamilyCore.
var familyTables = [numFamilies][]string{
	FamilySpans:   {"spans", "trace_summaries", "spans_staging", "aggregates", "error_samples"},
	FamilyLogs:    {"logs"},
	FamilyMetrics: {"metrics"},
	FamilyPrompts: {"prompt_records"},
}

var familyNames = [numFamilies]string{
	FamilyCore:    "core",
	FamilySpans:   "spans",
	FamilyLogs:    "logs",
	FamilyMetrics: "metrics",
	FamilyPrompts: "prompts",
}

func (f Family) String() string {
	if f < 0 || f >= numFamilies {
		return "unknown"
	}
	return familyNames[f]
}

// Families returns every family, core first.
func Families() []Family {
	out := make([]Family, numFamilies)
	for i := range out {
		out[i] = Family(i)
	}
	return out
}

// Tables returns the tables that belong to f. For FamilyCore it returns nil:
// core owns every table no other family lists.
func (f Family) Tables() []string {
	if f < 0 || f >= numFamilies {
		return nil
	}
	return familyTables[f]
}

// TableFamily returns the family that owns table.
func TableFamily(table string) Family {
	for f, tables := range familyTables {
		for _, t := range tables {
			if t == table {
				return Family(f)
			}
		}
	}
	return FamilyCore
}
