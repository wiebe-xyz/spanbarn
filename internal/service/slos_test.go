package service

import (
	"bytes"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// fakeSLORepo keeps SLOs and alerts in memory and scopes every call to a project.
type fakeSLORepo struct {
	slos    map[int64]repository.SLO
	alerts  map[int64]repository.SLOBurnAlert
	next    int64
	sumErr  error
	counts  func(from, to time.Time) (int64, int64)
	failAll error
}

func newFakeSLORepo() *fakeSLORepo {
	return &fakeSLORepo{slos: map[int64]repository.SLO{}, alerts: map[int64]repository.SLOBurnAlert{}}
}

func (f *fakeSLORepo) id() int64 { f.next++; return f.next }

func (f *fakeSLORepo) ListSLOs(p int64) ([]repository.SLO, error) {
	var out []repository.SLO
	for _, s := range f.slos {
		if s.ProjectID == p {
			out = append(out, s)
		}
	}
	return out, f.failAll
}

func (f *fakeSLORepo) GetSLO(p, id int64) (*repository.SLO, error) {
	if f.failAll != nil {
		return nil, f.failAll
	}
	s, ok := f.slos[id]
	if !ok || s.ProjectID != p {
		return nil, repository.ErrNotFound
	}
	return &s, nil
}

func (f *fakeSLORepo) CreateSLO(s repository.SLO) (int64, error) {
	for _, o := range f.slos {
		if o.ProjectID == s.ProjectID && o.Name == s.Name {
			return 0, repository.ErrConflict
		}
	}
	s.ID = f.id()
	f.slos[s.ID] = s
	return s.ID, f.failAll
}

func (f *fakeSLORepo) UpdateSLO(s repository.SLO) error {
	if _, err := f.GetSLO(s.ProjectID, s.ID); err != nil {
		return err
	}
	f.slos[s.ID] = s
	return nil
}

func (f *fakeSLORepo) DeleteSLO(p, id int64) error {
	if _, err := f.GetSLO(p, id); err != nil {
		return err
	}
	delete(f.slos, id)
	return nil
}

func (f *fakeSLORepo) ListSLOBurnAlerts(p, sloID int64) ([]repository.SLOBurnAlert, error) {
	var out []repository.SLOBurnAlert
	for _, a := range f.alerts {
		if a.SLOID == sloID && f.slos[sloID].ProjectID == p {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeSLORepo) GetSLOBurnAlert(p, id int64) (*repository.SLOBurnAlert, error) {
	a, ok := f.alerts[id]
	if !ok || f.slos[a.SLOID].ProjectID != p {
		return nil, repository.ErrNotFound
	}
	return &a, nil
}

func (f *fakeSLORepo) CreateSLOBurnAlert(p int64, a repository.SLOBurnAlert) (int64, error) {
	if _, err := f.GetSLO(p, a.SLOID); err != nil {
		return 0, err
	}
	a.ID = f.id()
	f.alerts[a.ID] = a
	return a.ID, nil
}

func (f *fakeSLORepo) UpdateSLOBurnAlert(p int64, a repository.SLOBurnAlert) error {
	if _, err := f.GetSLOBurnAlert(p, a.ID); err != nil {
		return err
	}
	f.alerts[a.ID] = a
	return nil
}

func (f *fakeSLORepo) DeleteSLOBurnAlert(p, id int64) error {
	if _, err := f.GetSLOBurnAlert(p, id); err != nil {
		return err
	}
	delete(f.alerts, id)
	return nil
}

func (f *fakeSLORepo) SumSLOCounts(_ int64, from, to time.Time) (int64, int64, error) {
	if f.sumErr != nil {
		return 0, 0, f.sumErr
	}
	if f.counts == nil {
		return 0, 0, nil
	}
	g, t := f.counts(from, to)
	return g, t, nil
}

func sloSvc() (*SLOService, *fakeSLORepo, *bytes.Buffer) {
	repo := newFakeSLORepo()
	var logs bytes.Buffer
	svc := NewSLOService(repo, slog.New(slog.NewJSONHandler(&logs, nil)))
	svc.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	return svc, repo, &logs
}

func validSLO() SLOInput {
	return SLOInput{
		Name:        "Checkout availability",
		GoodFilter:  []byte(`{"filters":[{"key":"status","op":"!=","value":"error"}]}`),
		TotalFilter: []byte(`{"filters":[{"key":"service","op":"=","value":"checkout"}]}`),
		Target:      0.995,
		WindowDays:  30,
	}
}

func TestSLOInputValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SLOInput)
	}{
		{"empty name", func(i *SLOInput) { i.Name = "  " }},
		{"long name", func(i *SLOInput) { i.Name = strings.Repeat("a", maxSLOName+1) }},
		{"target zero", func(i *SLOInput) { i.Target = 0 }},
		{"target one", func(i *SLOInput) { i.Target = 1 }},
		{"target above one", func(i *SLOInput) { i.Target = 1.5 }},
		{"target NaN", func(i *SLOInput) { i.Target = math.NaN() }},
		{"window zero", func(i *SLOInput) { i.WindowDays = 0 }},
		{"window above range", func(i *SLOInput) { i.WindowDays = 91 }},
		{"good filter malformed", func(i *SLOInput) { i.GoodFilter = []byte(`{"filters":`) }},
		{"good filter unknown op", func(i *SLOInput) {
			i.GoodFilter = []byte(`{"filters":[{"key":"status","op":"~","value":"x"}]}`)
		}},
		{"total filter unknown field", func(i *SLOInput) { i.TotalFilter = []byte(`{"nope":1}`) }},
	}
	for _, c := range cases {
		svc, repo, logs := sloSvc()
		in := validSLO()
		c.mutate(&in)
		_, err := svc.CreateSLO(1, in)
		if !errors.Is(err, filter.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
		if len(repo.slos) != 0 || logs.Len() != 0 {
			t.Errorf("%s: stored %d SLOs, logged %q", c.name, len(repo.slos), logs.String())
		}
	}
	svc, _, _ := sloSvc()
	if _, err := svc.CreateSLO(0, validSLO()); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("no project: err = %v", err)
	}
}

func TestSLOEmptyFiltersAreStoredAsEmptyGroups(t *testing.T) {
	svc, repo, _ := sloSvc()
	in := validSLO()
	in.GoodFilter, in.TotalFilter = nil, []byte("null")
	id, err := svc.CreateSLO(1, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(repo.slos[id].GoodFilter) + string(repo.slos[id].TotalFilter); got != "{}{}" {
		t.Fatalf("stored filters = %s", got)
	}
}

func TestSLOCrudAndProjectScoping(t *testing.T) {
	svc, _, _ := sloSvc()
	id, err := svc.CreateSLO(1, validSLO())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSLO(1, validSLO()); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := svc.CreateSLO(2, validSLO()); err != nil {
		t.Fatalf("same name in another project: %v", err)
	}
	if list, _ := svc.ListSLOs(1); len(list) != 1 {
		t.Fatalf("project 1 list = %d", len(list))
	}
	if list, err := svc.ListSLOs(3); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty project list = %v, %v", list, err)
	}

	in := validSLO()
	in.Name, in.Target = "Renamed", 0.99
	if err := svc.UpdateSLO(1, id, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.GetSLO(1, id); got.Name != "Renamed" || got.Target != 0.99 {
		t.Fatalf("updated = %+v", got)
	}

	// Project 3 sees none of it.
	if _, err := svc.GetSLO(3, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("get across projects: %v", err)
	}
	if err := svc.UpdateSLO(3, id, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("update across projects: %v", err)
	}
	if err := svc.DeleteSLO(3, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete across projects: %v", err)
	}
	if _, err := svc.Status(3, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("status across projects: %v", err)
	}
	if err := svc.DeleteSLO(1, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSLO(1, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestSLOUnexpectedFailuresAreLoggedAtError(t *testing.T) {
	svc, repo, logs := sloSvc()
	repo.failAll = errors.New("disk on fire")
	if _, err := svc.ListSLOs(1); err == nil {
		t.Fatal("expected the error to pass through")
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "disk on fire") {
		t.Fatalf("log = %q", logs.String())
	}
}

func burnInput() BurnAlertInput {
	return BurnAlertInput{WindowMinutes: 60, BurnRate: 14.4, WebhookURL: "https://hooks.example.com/x", Email: "ops@example.com"}
}

func TestBurnAlertValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*BurnAlertInput)
	}{
		{"window zero", func(i *BurnAlertInput) { i.WindowMinutes = 0 }},
		{"window equals SLO window", func(i *BurnAlertInput) { i.WindowMinutes = 30 * 24 * 60 }},
		{"window longer than SLO window", func(i *BurnAlertInput) { i.WindowMinutes = 31 * 24 * 60 }},
		{"burn rate zero", func(i *BurnAlertInput) { i.BurnRate = 0 }},
		{"burn rate negative", func(i *BurnAlertInput) { i.BurnRate = -1 }},
		{"burn rate NaN", func(i *BurnAlertInput) { i.BurnRate = math.NaN() }},
		{"negative cooldown", func(i *BurnAlertInput) { i.CooldownMinutes = -5 }},
		{"ftp webhook", func(i *BurnAlertInput) { i.WebhookURL = "ftp://example.com/x" }},
		{"schemeless webhook", func(i *BurnAlertInput) { i.WebhookURL = "example.com/x" }},
		{"javascript webhook", func(i *BurnAlertInput) { i.WebhookURL = "javascript:alert(1)" }},
		{"bad email", func(i *BurnAlertInput) { i.Email = "not an address" }},
	}
	for _, c := range cases {
		svc, repo, _ := sloSvc()
		id, _ := svc.CreateSLO(1, validSLO())
		in := burnInput()
		c.mutate(&in)
		if _, err := svc.CreateBurnAlert(1, id, in); !errors.Is(err, filter.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
		if len(repo.alerts) != 0 {
			t.Errorf("%s: stored an alert", c.name)
		}
	}
}

func TestBurnAlertCrudAndScoping(t *testing.T) {
	svc, _, _ := sloSvc()
	sloA, _ := svc.CreateSLO(1, validSLO())
	sloB, _ := svc.CreateSLO(1, SLOInput{Name: "Other", Target: 0.9, WindowDays: 7})
	alertID, err := svc.CreateBurnAlert(1, sloA, burnInput())
	if err != nil {
		t.Fatal(err)
	}
	alerts, _ := svc.ListBurnAlerts(1, sloA)
	if len(alerts) != 1 || !alerts[0].Enabled || alerts[0].CooldownMinutes != DefaultBurnCooldownMinutes {
		t.Fatalf("alerts = %+v", alerts)
	}

	off := false
	in := burnInput()
	in.Enabled, in.CooldownMinutes = &off, 10
	if err := svc.UpdateBurnAlert(1, sloA, alertID, in); err != nil {
		t.Fatal(err)
	}
	if alerts, _ = svc.ListBurnAlerts(1, sloA); alerts[0].Enabled || alerts[0].CooldownMinutes != 10 {
		t.Fatalf("after update = %+v", alerts[0])
	}

	// Another project, and another SLO of the same project, cannot reach the alert.
	if _, err := svc.ListBurnAlerts(2, sloA); !errors.Is(err, ErrNotFound) {
		t.Errorf("list across projects: %v", err)
	}
	if _, err := svc.CreateBurnAlert(2, sloA, burnInput()); !errors.Is(err, ErrNotFound) {
		t.Errorf("create across projects: %v", err)
	}
	if err := svc.UpdateBurnAlert(2, sloA, alertID, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("update across projects: %v", err)
	}
	if err := svc.UpdateBurnAlert(1, sloB, alertID, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("update through the wrong SLO: %v", err)
	}
	if err := svc.DeleteBurnAlert(1, sloB, alertID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete through the wrong SLO: %v", err)
	}
	if err := svc.DeleteBurnAlert(2, sloA, alertID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete across projects: %v", err)
	}
	if err := svc.DeleteBurnAlert(1, sloA, alertID); err != nil {
		t.Fatal(err)
	}
	if alerts, _ = svc.ListBurnAlerts(1, sloA); len(alerts) != 0 {
		t.Fatalf("alerts after delete = %+v", alerts)
	}
}

func TestSLOStatusZeroTrafficIsFullBudget(t *testing.T) {
	svc, _, _ := sloSvc()
	id, _ := svc.CreateSLO(1, validSLO())
	if _, err := svc.CreateBurnAlert(1, id, burnInput()); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.BudgetRemaining != 1 || st.Total != 0 || len(st.Alerts) != 1 || st.Alerts[0].CurrentBurn != 0 || st.Alerts[0].Firing {
		t.Fatalf("status = %+v", st)
	}
}

func TestSLOStatusUsesTheSLOWindowAndEachAlertWindow(t *testing.T) {
	svc, repo, _ := sloSvc()
	id, _ := svc.CreateSLO(1, validSLO())
	alertID, _ := svc.CreateBurnAlert(1, id, burnInput())
	a := repo.alerts[alertID]
	a.Firing = true
	repo.alerts[alertID] = a

	repo.counts = func(from, to time.Time) (int64, int64) {
		switch to.Sub(from) {
		case 30 * 24 * time.Hour:
			return 997, 1000 // 0.3% bad against a 0.5% budget
		case time.Hour:
			return 95, 100 // 5% bad, ten times the pace
		}
		t.Errorf("unexpected window %v", to.Sub(from))
		return 0, 0
	}
	st, err := svc.Status(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Good != 997 || st.Total != 1000 || math.Abs(st.BudgetRemaining-0.4) > 1e-9 || st.Target != 0.995 {
		t.Fatalf("status = %+v", st)
	}
	got := st.Alerts[0]
	if math.Abs(got.CurrentBurn-10) > 1e-9 || got.Good != 95 || got.Total != 100 || !got.Firing || got.WindowMinutes != 60 {
		t.Fatalf("alert status = %+v", got)
	}
}

func TestSLOStatusCountFailureIsLogged(t *testing.T) {
	svc, repo, logs := sloSvc()
	id, _ := svc.CreateSLO(1, validSLO())
	repo.sumErr = errors.New("db locked")
	if _, err := svc.Status(1, id); err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("log = %q", logs.String())
	}
}
