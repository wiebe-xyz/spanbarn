package repository

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// DBSpace samples the database's space usage. It is cheap — a few pragmas
// that read each file's header plus one statfs — and safe to call on every
// retention cycle.
//
// It covers every file the handle has open: main plus each attached file (the
// spans file in the split layout), and every time-shard file. Pages, file and WAL sizes are summed over
// them, so the ladder reacts to the total the database occupies on the volume.
// PageSize is main's; PageCount and FreelistCount are in main-sized pages.
// AutoVacuum is the lowest mode of any file, so a file that cannot return
// freed pages still raises the warning.
//
// dbPath may be ":memory:" or empty, in which case the filesystem figures are
// left at zero and Measured() reports false.
func (r *Repository) DBSpace(ctx context.Context, dbPath string) (engine.Space, error) {
	var addShards func(context.Context, *engine.Space) error
	if r.shards != nil {
		addShards = r.shards.addSpace
	}
	return engine.MeasureSpace(ctx, r.db, dbPath, addShards)
}
