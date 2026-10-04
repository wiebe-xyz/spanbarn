package alert

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	goodFilterJSON  = `{"match":"and","filters":[{"key":"tag","op":"=","value":"good"}]}`
	totalFilterJSON = `{"match":"and","filters":[{"key":"tag","op":"=","value":"total"}]}`
)

type fakeSpan struct {
	at    time.Time
	good  bool
	isErr bool
}

// fakeSLORepo is an in-memory SLORepository. Spans carry whether they match the
// good filter; every span matches the total filter.
type fakeSLORepo struct {
	slos        []repository.SLO
	alerts      map[int64][]repository.SLOBurnAlert // by SLO id
	spans       map[int64][]fakeSpan                // by project id
	counts      map[int64]map[time.Time]repository.SLOCount
	inserted    []repository.SLOCount
	pruned      []time.Time
	countErr    map[int64]error // by project id
	stateWrites int
}

func newFakeSLORepo() *fakeSLORepo {
	return &fakeSLORepo{
		alerts: map[int64][]repository.SLOBurnAlert{},
		spans:  map[int64][]fakeSpan{},
		counts: map[int64]map[time.Time]repository.SLOCount{},
	}
}

func (f *fakeSLORepo) ListSLOs(projectID int64) ([]repository.SLO, error) {
	var out []repository.SLO
	for _, s := range f.slos {
		if s.ProjectID == projectID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSLORepo) ListSLOBurnAlerts(_, sloID int64) ([]repository.SLOBurnAlert, error) {
	return append([]repository.SLOBurnAlert(nil), f.alerts[sloID]...), nil
}

func (f *fakeSLORepo) CountSpans(q repository.SpanFilter) (int64, int64, error) {
	if err := f.countErr[q.ProjectID]; err != nil {
		return 0, 0, err
	}
	wantGood := q.Expr != nil && len(q.Expr.Filters) == 1 && q.Expr.Filters[0].Value == "good"
	var total, errs int64
	for _, s := range f.spans[q.ProjectID] {
		if s.at.Before(q.From) || s.at.After(q.To) || (wantGood && !s.good) {
			continue
		}
		total++
		if s.isErr {
			errs++
		}
	}
	return total, errs, nil
}

func (f *fakeSLORepo) LatestSLOBucket(sloID int64) (time.Time, error) {
	var latest time.Time
	for b := range f.counts[sloID] {
		if b.After(latest) {
			latest = b
		}
	}
	return latest, nil
}

func (f *fakeSLORepo) SumSLOCounts(sloID int64, from, to time.Time) (int64, int64, error) {
	var good, total int64
	for b, c := range f.counts[sloID] {
		if !b.Before(from) && b.Before(to) {
			good += c.Good
			total += c.Total
		}
	}
	return good, total, nil
}

func (f *fakeSLORepo) InsertSLOCounts(counts []repository.SLOCount) error {
	for _, c := range counts {
		if f.counts[c.SLOID] == nil {
			f.counts[c.SLOID] = map[time.Time]repository.SLOCount{}
		}
		f.counts[c.SLOID][c.BucketStart] = c
		f.inserted = append(f.inserted, c)
	}
	return nil
}

func (f *fakeSLORepo) UpdateSLOBurnAlertState(id int64, firing bool, at time.Time) error {
	f.stateWrites++
	for sid, list := range f.alerts {
		for i := range list {
			if list[i].ID != id {
				continue
			}
			f.alerts[sid][i].Firing = firing
			if !at.IsZero() {
				f.alerts[sid][i].LastTriggeredAt = sql.NullTime{Time: at, Valid: true}
			}
		}
	}
	return nil
}

func (f *fakeSLORepo) DeleteSLOCountsBefore(cutoff time.Time) (int64, error) {
	f.pruned = append(f.pruned, cutoff)
	return 0, nil
}

type sloMailbox struct {
	webhooks []SLOPayload
	urls     []string
	emails   []string
	failWeb  bool
	failMail bool
}

func (m *sloMailbox) SendSLOWebhook(_ context.Context, url string, p SLOPayload) error {
	if m.failWeb {
		return errors.New("webhook down")
	}
	m.urls = append(m.urls, url)
	m.webhooks = append(m.webhooks, p)
	return nil
}

func (m *sloMailbox) SendEmail(_ context.Context, to, _, _ string) error {
	if m.failMail {
		return errors.New("smtp down")
	}
	m.emails = append(m.emails, to)
	return nil
}

type fixedRatio int

func (r fixedRatio) Ratio(context.Context, int64, string) int { return int(r) }

type sloHarness struct {
	repo *fakeSLORepo
	mail *sloMailbox
	ev   *SLOEvaluator
	logs *bytes.Buffer
	t    *testing.T
}

// base is a minute boundary; ticks run a few seconds past later boundaries.
var base = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func newSLOHarness(t *testing.T, ratio SampleRatioLookup) *sloHarness {
	t.Helper()
	repo := newFakeSLORepo()
	mail := &sloMailbox{}
	logs := &bytes.Buffer{}
	ev := NewSLOEvaluator(repo, repo, mail, slog.New(slog.NewJSONHandler(logs, nil)), ratio)
	return &sloHarness{repo: repo, mail: mail, ev: ev, logs: logs, t: t}
}

func (h *sloHarness) addSLO(id, project int64, target float64, windowDays int) {
	h.repo.slos = append(h.repo.slos, repository.SLO{
		ID: id, ProjectID: project, Name: "availability", Target: target, WindowDays: windowDays,
		GoodFilter: []byte(goodFilterJSON), TotalFilter: []byte(totalFilterJSON),
	})
}

func (h *sloHarness) addAlert(sloID int64, a repository.SLOBurnAlert) {
	a.SLOID = sloID
	h.repo.alerts[sloID] = append(h.repo.alerts[sloID], a)
}

// addTraffic adds good and bad spans inside the minute starting at minuteStart.
func (h *sloHarness) addTraffic(project int64, minuteStart time.Time, good, bad int) {
	for i := 0; i < good; i++ {
		h.repo.spans[project] = append(h.repo.spans[project], fakeSpan{at: minuteStart.Add(time.Duration(i) * time.Millisecond), good: true})
	}
	for i := 0; i < bad; i++ {
		h.repo.spans[project] = append(h.repo.spans[project], fakeSpan{at: minuteStart.Add(time.Second + time.Duration(i)*time.Millisecond), isErr: true})
	}
}

func (h *sloHarness) tick(now time.Time) {
	h.ev.now = func() time.Time { return now }
	h.ev.Run(context.Background(), []int64{1})
}

func (h *sloHarness) restart() {
	h.ev = NewSLOEvaluator(h.repo, h.repo, h.mail, slog.New(slog.NewJSONHandler(h.logs, nil)), h.ev.ratioLookup)
}

func burnAlert(id int64, window int, rate float64) repository.SLOBurnAlert {
	return repository.SLOBurnAlert{ID: id, WindowMinutes: window, BurnRate: rate, WebhookURL: "https://hook.test/" + "x", Enabled: true}
}

func TestSLOCountsClosedBucketsOnly(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.addTraffic(1, base.Add(-time.Minute), 8, 2)
	h.addTraffic(1, base, 5, 0) // 12:00 bucket ends 12:01:00 and closes at 12:01:30

	h.tick(base.Add(80 * time.Second))

	if _, ok := h.repo.counts[1][base]; ok {
		t.Fatal("bucket inside the ingest lag was counted")
	}
	got := h.repo.counts[1][base.Add(-time.Minute)]
	if got.Good != 8 || got.Total != 10 {
		t.Fatalf("bucket 11:59 = %+v, want 8/10", got)
	}

	h.tick(base.Add(95 * time.Second))
	got = h.repo.counts[1][base]
	if got.Good != 5 || got.Total != 5 {
		t.Fatalf("bucket 12:00 after lag = %+v, want 5/5", got)
	}
}

func TestSLOSamplingScalesOkSpansOnly(t *testing.T) {
	h := newSLOHarness(t, fixedRatio(10))
	h.addSLO(1, 1, 0.9, 30)
	// 8 kept ok spans stand for 80; the 2 error spans were all kept.
	h.addTraffic(1, base.Add(-time.Minute), 8, 2)

	h.tick(base.Add(40 * time.Second))

	got := h.repo.counts[1][base.Add(-time.Minute)]
	if got.Good != 80 || got.Total != 82 {
		t.Fatalf("scaled bucket = %+v, want 80/82", got)
	}
}

func TestSLONoDoubleCountAcrossTicksAndRestart(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.addTraffic(1, base.Add(-time.Minute), 4, 0)

	h.tick(base.Add(40 * time.Second))
	h.tick(base.Add(45 * time.Second)) // same closed range again
	h.restart()
	h.tick(base.Add(50 * time.Second)) // after a restart
	h.addTraffic(1, base, 3, 0)
	h.restart()
	h.tick(base.Add(100 * time.Second)) // next bucket closes

	seen := map[time.Time]int{}
	for _, c := range h.repo.inserted {
		seen[c.BucketStart]++
	}
	for b, n := range seen {
		if n != 1 {
			t.Errorf("bucket %s inserted %d times", b, n)
		}
	}
	good, total, _ := h.repo.SumSLOCounts(1, base.Add(-time.Hour), base.Add(time.Hour))
	if good != 7 || total != 7 {
		t.Fatalf("sum = %d/%d, want 7/7", good, total)
	}
}

func TestSLORestartResumesAfterOutageWithoutHole(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.tick(base.Add(40 * time.Second))
	before := len(h.repo.counts[1])

	h.restart()
	h.tick(base.Add(10*time.Minute + 40*time.Second))

	// Buckets 12:00 .. 12:09 close in between, each stored exactly once.
	if got := len(h.repo.counts[1]) - before; got != 10 {
		t.Fatalf("resumed %d buckets, want 10", got)
	}
	for i := 0; i < 10; i++ {
		if _, ok := h.repo.counts[1][base.Add(time.Duration(i)*time.Minute)]; !ok {
			t.Errorf("hole at %s", base.Add(time.Duration(i)*time.Minute))
		}
	}
}

func TestSLOCatchUpIsCapped(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.repo.counts[1] = map[time.Time]repository.SLOCount{base.Add(-5 * time.Hour): {SLOID: 1, BucketStart: base.Add(-5 * time.Hour)}}

	h.tick(base.Add(40 * time.Second))

	if len(h.repo.inserted) != sloMaxCatchUp {
		t.Fatalf("counted %d buckets after a long outage, want %d", len(h.repo.inserted), sloMaxCatchUp)
	}
}

func TestSLOBurnAlertFiresOnceThenResets(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	a := burnAlert(7, 5, 2)
	a.Email = "ops@example.com"
	h.addAlert(1, a)
	// 50% bad against a 10% budget is a burn rate of 5.
	for i := 1; i <= 3; i++ {
		h.addTraffic(1, base.Add(-time.Duration(i)*time.Minute), 5, 5)
	}

	h.tick(base.Add(40 * time.Second))
	if len(h.mail.webhooks) != 1 || len(h.mail.emails) != 1 {
		t.Fatalf("first tick sent %d webhooks %d emails, want 1 and 1", len(h.mail.webhooks), len(h.mail.emails))
	}
	p := h.mail.webhooks[0]
	if p.SLOID != 1 || p.AlertID != 7 || p.Name != "availability" || p.WindowMinutes != 5 || p.Threshold != 2 || p.BurnRate < 4.99 || p.BurnRate > 5.01 {
		t.Fatalf("payload = %+v", p)
	}
	if p.BudgetRemaining >= 0 {
		t.Fatalf("budget remaining = %v, want overspent", p.BudgetRemaining)
	}
	if !h.repo.alerts[1][0].Firing {
		t.Fatal("alert not marked firing")
	}

	h.tick(base.Add(100 * time.Second))
	if len(h.mail.webhooks) != 1 {
		t.Fatalf("still burning, sent %d webhooks, want no repeat", len(h.mail.webhooks))
	}

	// Healthy traffic for longer than the window pushes the burn below 2.
	for i := 0; i < 8; i++ {
		h.addTraffic(1, base.Add(time.Duration(i)*time.Minute), 10, 0)
	}
	h.tick(base.Add(9*time.Minute + 40*time.Second))
	if h.repo.alerts[1][0].Firing {
		t.Fatal("firing flag not cleared on recovery")
	}
	if len(h.mail.webhooks) != 1 {
		t.Fatal("recovery must not notify")
	}
}

func TestSLOBurnAlertCooldownHoldsBackRefire(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	a := burnAlert(7, 2, 2)
	a.CooldownMinutes = 30
	a.LastTriggeredAt = sql.NullTime{Time: base.Add(-10 * time.Minute), Valid: true}
	h.addAlert(1, a)
	h.addTraffic(1, base.Add(-time.Minute), 0, 10)

	h.tick(base.Add(40 * time.Second))
	if len(h.mail.webhooks) != 0 {
		t.Fatal("notified inside cooldown")
	}
	if h.repo.alerts[1][0].Firing {
		t.Fatal("alert marked firing while the notification was held back")
	}

	// Keep burning past the cooldown end at 12:20.
	for i := 0; i < 25; i++ {
		h.addTraffic(1, base.Add(time.Duration(i)*time.Minute), 0, 10)
	}
	h.tick(base.Add(21*time.Minute + 40*time.Second))
	if len(h.mail.webhooks) != 1 {
		t.Fatalf("after cooldown sent %d webhooks, want 1", len(h.mail.webhooks))
	}
}

func TestSLOZeroTrafficStoresEmptyBucketsAndNeverFires(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.addAlert(1, burnAlert(7, 5, 1))

	h.tick(base.Add(40 * time.Second))

	if len(h.mail.webhooks) != 0 {
		t.Fatal("fired on zero traffic")
	}
	c, ok := h.repo.counts[1][base.Add(-time.Minute)]
	if !ok || c.Total != 0 {
		t.Fatalf("empty bucket not stored: %+v ok=%v", c, ok)
	}
}

func TestSLOZeroTrafficClearsFiringAlert(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	a := burnAlert(7, 2, 1)
	a.Firing = true
	h.addAlert(1, a)

	h.tick(base.Add(40 * time.Second))

	if h.repo.alerts[1][0].Firing {
		t.Fatal("burn is 0 with no traffic, firing flag should clear")
	}
}

func TestSLODisabledAlertIsSkipped(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	a := burnAlert(7, 5, 1)
	a.Enabled = false
	h.addAlert(1, a)
	h.addTraffic(1, base.Add(-time.Minute), 0, 10)

	h.tick(base.Add(40 * time.Second))

	if len(h.mail.webhooks) != 0 || h.repo.stateWrites != 0 {
		t.Fatal("disabled alert was evaluated")
	}
}

func TestSLOFailingSLODoesNotBlockOthersAndLogsError(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.repo.slos[0].GoodFilter = []byte(`{"filters":[{"key":"x","op":"bogus","value":"1"}]}`)
	h.addSLO(2, 1, 0.9, 30)
	h.addAlert(1, burnAlert(7, 5, 1))
	h.addAlert(2, burnAlert(8, 5, 1))
	h.addTraffic(1, base.Add(-time.Minute), 0, 10)

	h.tick(base.Add(40 * time.Second))

	if len(h.mail.webhooks) != 1 || h.mail.webhooks[0].SLOID != 2 {
		t.Fatalf("webhooks = %+v, want only SLO 2", h.mail.webhooks)
	}
	if !strings.Contains(h.logs.String(), `"level":"ERROR"`) || !strings.Contains(h.logs.String(), `"sloID":1`) {
		t.Fatalf("no error log for the failing SLO: %s", h.logs.String())
	}
}

func TestSLOCountFailureKeepsFiringStateAndSkipsPrune(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	a := burnAlert(7, 5, 1)
	a.Firing = true
	h.addAlert(1, a)
	h.repo.countErr = map[int64]error{1: errors.New("db busy")}

	h.tick(base.Add(40 * time.Second))

	if !h.repo.alerts[1][0].Firing {
		t.Fatal("firing flag cleared on a failed count")
	}
	if !strings.Contains(h.logs.String(), `"level":"ERROR"`) {
		t.Fatal("count failure not logged at error level")
	}
}

func TestSLONotifyFailureRetriesNextTick(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.addAlert(1, burnAlert(7, 5, 1))
	h.addTraffic(1, base.Add(-time.Minute), 0, 10)
	h.mail.failWeb = true

	h.tick(base.Add(40 * time.Second))
	if h.repo.alerts[1][0].Firing {
		t.Fatal("marked firing though nothing was delivered")
	}

	h.mail.failWeb = false
	h.tick(base.Add(100 * time.Second))
	if len(h.mail.webhooks) != 1 || !h.repo.alerts[1][0].Firing {
		t.Fatalf("retry sent %d webhooks, firing=%v", len(h.mail.webhooks), h.repo.alerts[1][0].Firing)
	}
}

func TestSLOOneChannelFailingDoesNotBlockTheOther(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	a := burnAlert(7, 5, 1)
	a.Email = "ops@example.com"
	h.addAlert(1, a)
	h.addTraffic(1, base.Add(-time.Minute), 0, 10)
	h.mail.failMail = true

	h.tick(base.Add(40 * time.Second))

	if len(h.mail.webhooks) != 1 || !h.repo.alerts[1][0].Firing {
		t.Fatal("webhook should deliver and mark firing though email failed")
	}
}

func TestSLOPrunesBeyondLongestWindow(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 7)
	h.addSLO(2, 1, 0.9, 30)

	now := base.Add(40 * time.Second)
	h.tick(now)

	if len(h.repo.pruned) != 1 {
		t.Fatalf("pruned %d times", len(h.repo.pruned))
	}
	if want := now.Add(-30 * 24 * time.Hour); !h.repo.pruned[0].Equal(want) {
		t.Fatalf("cutoff %s, want %s", h.repo.pruned[0], want)
	}
}

func TestRunnerTickEvaluatesSLOs(t *testing.T) {
	h := newSLOHarness(t, nil)
	h.addSLO(1, 1, 0.9, 30)
	h.addAlert(1, burnAlert(7, 5, 1))
	h.addTraffic(1, base.Add(-time.Minute), 0, 10)
	h.ev.now = func() time.Time { return base.Add(40 * time.Second) }

	r := NewRunner(NewEvaluator(newMockRepo(), &mockNotifier{}, nil, nil), staticProjects{1}, time.Minute, nil)
	r.SetSLOEvaluator(h.ev)
	r.tick(context.Background())

	if len(h.mail.webhooks) != 1 {
		t.Fatalf("runner tick sent %d SLO webhooks, want 1", len(h.mail.webhooks))
	}
}

type staticProjects []int64

func (s staticProjects) ListProjectIDs() ([]int64, error) { return s, nil }
