package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// retentionWorkDir holds the copies the retention run works on, so the
// generated layouts stay untouched for later commands.
const (
	retentionWorkDir = "retention-work"
	// retInsertEvery and retInsertSpans give the production ingest rate:
	// 8 batches of 8 spans per second, about 64 spans/s.
	retInsertEvery = 125 * time.Millisecond
	retInsertSpans = 8
	// retMinInsertRun keeps the insert loop going for at least this long, so a
	// fast expiry still yields a handful of insert samples.
	retMinInsertRun = time.Second
	// retWALPollEvery is how often the WAL file size is sampled for its peak.
	retWALPollEvery = 50 * time.Millisecond
)

// retResult is one layout's measurement of expiring the oldest day.
type retResult struct {
	wall        time.Duration
	rowsRemoved int64
	rowsLeft    int64 // rows older than the cutoff still in the file afterwards
	sizeBefore  int64
	sizeAfter   int64
	walPeak     int64
	freelist    int64 // growth of freelist_count * page_size in the written file
	checkpoint  time.Duration
	inserts     timings
	timedOut    bool
}

// retTable is one L1 retention method's share of the delete.
type retTable struct {
	table string
	rows  int64
	wall  time.Duration
	left  int64
	ran   bool
}

// cmdRetention expires the oldest day of data twice, on copies: with the
// production row DELETEs on the single file (L1) and by removing the oldest
// daily file (L3), each under a production-rate insert loop.
func cmdRetention(ctx context.Context, o options, out io.Writer) (err error) {
	ws := &retWork{dir: filepath.Join(o.dir, retentionWorkDir)}
	if err := os.Mkdir(ws.dir, 0o755); err != nil {
		return fmt.Errorf("retention work dir: %w", err)
	}
	defer func() {
		if cerr := ws.cleanup(); err == nil {
			err = cerr
		}
	}()
	l1, tables, err := expireSingle(ctx, o, ws)
	if err != nil {
		return fmt.Errorf("retention L1: %w", err)
	}
	l3, err := expireShards(ctx, o, ws)
	if err != nil {
		return fmt.Errorf("retention L3: %w", err)
	}
	printRetention(out, o, l1, l3, tables)
	return nil
}

// retWork tracks the files copied into the work dir, so cleanup removes
// exactly those (and the -wal/-shm SQLite made next to them) and nothing else.
type retWork struct {
	dir   string
	files []string
}

// copyIn copies a checkpointed database into the work dir under name.
func (w *retWork) copyIn(src, name string) (string, error) {
	if fi, err := os.Stat(src + "-wal"); err == nil && fi.Size() > 0 {
		return "", fmt.Errorf("%s has a pending WAL (%d bytes); checkpoint it first", src, fi.Size())
	}
	dst := filepath.Join(w.dir, name)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	w.files = append(w.files, dst)
	_, err = io.Copy(f, in)
	return dst, errors.Join(err, f.Close())
}

func (w *retWork) cleanup() error {
	var errs []error
	for _, f := range w.files {
		errs = append(errs, removeDBFiles(f))
	}
	errs = append(errs, os.Remove(w.dir))
	return errors.Join(errs...)
}

// removeDBFiles removes a database file and its -wal and -shm, by exact path.
func removeDBFiles(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// expireSingle runs the production retention deletes on a copy of the L1 file.
// The inserts go through the same *DB, because production has one writer
// connection and an insert queues behind a delete batch there too.
func expireSingle(ctx context.Context, o options, ws *retWork) (retResult, []retTable, error) {
	var res retResult
	path, err := ws.copyIn(filepath.Join(layoutDir(o.dir, layoutSingle), singleFile), "l1-"+singleFile)
	if err != nil {
		return res, nil, err
	}
	res.sizeBefore = retFileSize(path)
	db, err := repository.NewDB(path)
	if err != nil {
		return res, nil, err
	}
	defer db.Close()
	freeBefore, err := freelistBytes(ctx, db.DB)
	if err != nil {
		return res, nil, err
	}
	cutoff := o.window().Start.Add(24 * time.Hour)
	repo := repository.NewRepository(db.DB)
	tables, err := measureExpiry(ctx, &res, path, repo, func() ([]retTable, error) {
		dctx, cancel := context.WithTimeout(ctx, o.duration)
		defer cancel()
		return runRetentionDeletes(dctx, repo, cutoff)
	})
	if err != nil {
		return res, nil, err
	}
	for _, t := range tables {
		res.rowsRemoved += t.rows
		res.timedOut = res.timedOut || !t.ran
	}
	if err := finishWritten(ctx, &res, db.DB, freeBefore, func() int64 { return retFileSize(path) }); err != nil {
		return res, nil, err
	}
	res.rowsLeft, err = countLeft(ctx, db.DB, tables, cutoff)
	return res, tables, err
}

// measureExpiry times expire with the insert loop and the WAL watch around it.
func measureExpiry(ctx context.Context, res *retResult, written string, repo *repository.Repository, expire func() ([]retTable, error)) ([]retTable, error) {
	wal := watchSize(written + "-wal")
	loop := startInsertLoop(ctx, repo)
	start := time.Now()
	tables, err := expire()
	res.wall = time.Since(start)
	lat, lerr := loop.stop()
	res.walPeak = wal.stop()
	res.inserts = lat
	return tables, errors.Join(err, lerr)
}

// finishWritten times the TRUNCATE checkpoint of the written file, then records
// the data size after it and how much the freelist grew.
func finishWritten(ctx context.Context, res *retResult, db *sql.DB, freeBefore int64, size func() int64) error {
	start := time.Now()
	if err := checkpoint(ctx, db); err != nil {
		return err
	}
	res.checkpoint = time.Since(start)
	free, err := freelistBytes(ctx, db)
	res.freelist = free - freeBefore
	res.sizeAfter = size()
	return err
}

// retDelete is one production retention method, bound to the cutoff.
type retDelete struct {
	table string
	run   func(context.Context) (int64, error)
}

// retentionDeletes lists the methods internal/retention uses to drop rows older
// than a cutoff. The ctx-taking ones loop 1000-row batches until a short one, so
// one call drains a table; spans is one unbatched statement. prompt_records is
// drained in the per-cycle chunks internal/retention uses.
func retentionDeletes(repo *repository.Repository, cutoff time.Time) []retDelete {
	return []retDelete{
		{"spans", func(context.Context) (int64, error) { return repo.DeleteSpansOlderThan(cutoff) }},
		{"trace_summaries", func(c context.Context) (int64, error) { return repo.DeleteTraceSummariesOlderThan(c, cutoff, cutoff) }},
		{"logs", func(c context.Context) (int64, error) { return repo.DeleteLogsOlderThan(c, cutoff, cutoff) }},
		{"metrics", func(c context.Context) (int64, error) { return repo.DeleteMetricsOlderThan(c, cutoff) }},
		{"error_samples", func(c context.Context) (int64, error) { return repo.DeleteErrorSamplesOlderThan(c, cutoff) }},
		{"prompt_records", func(c context.Context) (int64, error) { return drainPrompts(c, repo, cutoff) }},
	}
}

// retPromptChunk matches maxPromptRowsPerCycle in internal/retention.
const retPromptChunk = 20_000

// drainPrompts calls the limited prompt delete until nothing older than cutoff
// remains, which in production takes one retention cycle per chunk.
func drainPrompts(ctx context.Context, repo *repository.Repository, cutoff time.Time) (int64, error) {
	var total int64
	for {
		n, more, err := repo.DeletePromptRecordsOlderThanLimited(ctx, cutoff, retPromptChunk)
		total += n
		if err != nil || !more {
			return total, err
		}
	}
}

// runRetentionDeletes runs each delete until ctx expires; a delete the deadline cut short is reported as unfinished.
func runRetentionDeletes(ctx context.Context, repo *repository.Repository, cutoff time.Time) ([]retTable, error) {
	var out []retTable
	for _, d := range retentionDeletes(repo, cutoff) {
		t := retTable{table: d.table}
		if ctx.Err() == nil {
			start := time.Now()
			n, err := d.run(ctx)
			t.rows, t.wall = n, time.Since(start)
			if err != nil && ctx.Err() == nil {
				return out, fmt.Errorf("delete %s: %w", d.table, err)
			}
			t.ran = err == nil
		}
		out = append(out, t)
	}
	return out, nil
}

// countLeft counts the rows still older than the cutoff: rows kept on purpose
// (logs of error traces, summaries with expires_at) plus what a timeout left.
func countLeft(ctx context.Context, db *sql.DB, tables []retTable, cutoff time.Time) (int64, error) {
	var total int64
	for i := range tables {
		t := &tables[i]
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s < ?", t.table, timeColumn[t.table])
		if err := db.QueryRowContext(ctx, q, sqlTime(cutoff)).Scan(&t.left); err != nil {
			return 0, fmt.Errorf("count left in %s: %w", t.table, err)
		}
		total += t.left
	}
	return total, nil
}

// expireShards copies the L3 files and expires the oldest day by removing its
// file, while the insert loop writes to the newest day file.
func expireShards(ctx context.Context, o options, ws *retWork) (retResult, error) {
	var res retResult
	copies, err := copyShards(o, ws)
	if err != nil {
		return res, err
	}
	oldest, newest := copies[1], copies[len(copies)-1]
	res.sizeBefore = retFileSize(copies...)
	if res.rowsRemoved, err = countAllRows(ctx, oldest); err != nil {
		return res, err
	}
	db, err := repository.NewDB(newest)
	if err != nil {
		return res, err
	}
	defer db.Close()
	freeBefore, err := freelistBytes(ctx, db.DB)
	if err != nil {
		return res, err
	}
	_, err = measureExpiry(ctx, &res, newest, repository.NewRepository(db.DB), func() ([]retTable, error) {
		return nil, removeDBFiles(oldest)
	})
	if err != nil {
		return res, err
	}
	return res, finishWritten(ctx, &res, db.DB, freeBefore, func() int64 { return retFileSize(copies...) })
}

// copyShards copies config.db, then every day file oldest first, in that order.
func copyShards(o options, ws *retWork) ([]string, error) {
	src := layoutDir(o.dir, layoutShards)
	days, err := dataFiles(src)
	if err != nil {
		return nil, err
	}
	if len(days) < 2 {
		return nil, fmt.Errorf("need at least 2 day files, found %d", len(days))
	}
	cfg, err := ws.copyIn(filepath.Join(src, configFile), "l3-"+configFile)
	if err != nil {
		return nil, err
	}
	out := []string{cfg}
	for _, d := range days {
		c, err := ws.copyIn(d, "l3-"+filepath.Base(d))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// countAllRows sums the in-scope rows of one data file.
func countAllRows(ctx context.Context, path string) (int64, error) {
	db, err := repository.NewReadOnlyDB(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var total int64
	for _, t := range inScope {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return 0, fmt.Errorf("count %s in %s: %w", t, path, err)
		}
		total += n
	}
	return total, nil
}

// freelistBytes is the space SQLite keeps inside the file while auto_vacuum is NONE.
func freelistBytes(ctx context.Context, db *sql.DB) (int64, error) {
	var pages, size int64
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&pages); err != nil {
		return 0, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return 0, err
	}
	return pages * size, nil
}

// retFileSize is the total size of paths; a missing file counts as 0.
func retFileSize(paths ...string) int64 {
	var total int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func printRetention(out io.Writer, o options, l1, l3 retResult, tables []retTable) {
	cutoff := o.window().Start.Add(24 * time.Hour)
	fmt.Fprintf(out, "## Retention: expire the oldest day\n\n")
	fmt.Fprintf(out, "Expire every row older than %s (the oldest of %d days), on copies of the generated files. "+
		"During the expiry a loop inserts %d spans every %s: on L1 through the same writer connection as the deletes, "+
		"on L3 into the newest day file. The L1 delete phase is capped at %s.\n\n",
		sqlTime(cutoff), o.days, retInsertSpans, retInsertEvery, o.duration)
	fmt.Fprintln(out, "| metric | L1 row DELETE | L3 file delete |")
	fmt.Fprintln(out, "|---|---:|---:|")
	for _, r := range retentionRows(o, l1, l3) {
		fmt.Fprintf(out, "| %s | %s | %s |\n", r[0], r[1], r[2])
	}
	fmt.Fprintf(out, "\n### L1 row DELETE per table\n\n")
	fmt.Fprintln(out, "| table | rows deleted | wall time | rows older than cutoff left |")
	fmt.Fprintln(out, "|---|---:|---:|---:|")
	for _, t := range tables {
		fmt.Fprintf(out, "| %s | %d | %s | %d |\n", t.table, t.rows, retTableWall(t), t.left)
	}
	fmt.Fprintln(out)
}

func retentionRows(o options, l1, l3 retResult) [][3]string {
	row := func(name string, f func(retResult) string) [3]string { return [3]string{name, f(l1), f(l3)} }
	return [][3]string{
		row("result", func(r retResult) string { return retStatus(r, o.duration) }),
		row("wall time", func(r retResult) string { return ms(r.wall) }),
		row("rows removed", func(r retResult) string { return fmt.Sprint(r.rowsRemoved) }),
		row("rows older than cutoff left", func(r retResult) string { return fmt.Sprint(r.rowsLeft) }),
		row("data files before", func(r retResult) string { return retBytes(r.sizeBefore) }),
		row("data files after", func(r retResult) string { return retBytes(r.sizeAfter) }),
		row("bytes returned to the filesystem", func(r retResult) string { return retBytes(r.sizeBefore - r.sizeAfter) }),
		row("WAL peak (written file)", func(r retResult) string { return retBytes(r.walPeak) }),
		row("space left in freelist", func(r retResult) string { return retBytes(r.freelist) }),
		row("checkpoint (TRUNCATE) time", func(r retResult) string { return ms(r.checkpoint) }),
		row("insert p50", func(r retResult) string { return ms(r.inserts.pct(0.50)) }),
		row("insert p99", func(r retResult) string { return ms(r.inserts.pct(0.99)) }),
		row("insert max", func(r retResult) string { return ms(r.inserts.pct(1)) }),
		row("inserts during run", func(r retResult) string { return fmt.Sprint(len(r.inserts)) }),
	}
}

func retTableWall(t retTable) string {
	if !t.ran {
		return "not finished"
	}
	return ms(t.wall)
}

func retStatus(r retResult, limit time.Duration) string {
	if r.timedOut {
		return fmt.Sprintf("did not finish within %s, %d rows remained", limit, r.rowsLeft)
	}
	return "finished"
}

// retBytes formats a byte count in MiB with two decimals, keeping the sign.
func retBytes(n int64) string { return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20)) }
