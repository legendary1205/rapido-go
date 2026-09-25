// Package usagejob is the BACKEND-role-only loop that prunes node_user_usages,
// the per-user/per-node/per-hour traffic table that otherwise grows without
// bound (~100k rows/day at 9k users). Reported totals (users.used_traffic,
// admins.users_usage, node counters) are separate counters, so pruning only
// limits how far back a per-node usage window (?start=) can reach.
package usagejob

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

const (
	// BatchSize rows are deleted per statement so a large backlog never holds
	// one long transaction or produces one huge WAL burst.
	BatchSize = 20000
	// BatchPause lets autovacuum and the node-report writers breathe between
	// two full batches.
	BatchPause = 200 * time.Millisecond

	firstSweepDelay = time.Minute
)

// Queries is the part of generated.Querier this job uses.
type Queries interface {
	DeleteOldNodeUserUsages(ctx context.Context, arg generated.DeleteOldNodeUserUsagesParams) (int64, error)
}

// Maintenance reports whether a restore is in progress; *cache.Client
// satisfies it. A nil Maintenance never pauses.
type Maintenance interface {
	IsMaintenanceMode(ctx context.Context) (bool, error)
}

// Run sweeps every interval until ctx is canceled. retentionDays <= 0 disables
// the job (it returns immediately).
func Run(ctx context.Context, q Queries, maintenance Maintenance, retentionDays int, logger *slog.Logger, interval time.Duration) {
	if retentionDays <= 0 {
		logger.Info("usage retention disabled (USAGE_RETENTION_DAYS=0), node_user_usages is kept forever")
		return
	}
	retention := time.Duration(retentionDays) * 24 * time.Hour
	timer := time.NewTimer(firstSweepDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(interval)

		if maintenance != nil {
			if on, err := maintenance.IsMaintenanceMode(ctx); err != nil {
				logger.Error("usage retention: checking maintenance mode", "error", err)
				continue
			} else if on {
				continue
			}
		}
		removed, err := Sweep(ctx, q, time.Now().Add(-retention), BatchSize, BatchPause)
		if err != nil {
			logger.Error("usage retention sweep failed", "error", err, "removed_before_error", removed)
			continue
		}
		if removed > 0 {
			logger.Info("usage retention removed old node_user_usages rows", "rows", removed, "older_than_days", retentionDays)
		}
	}
}

// Sweep deletes every row older than cutoff in batches of batchSize, pausing
// between full batches, and returns how many rows it removed (also on error,
// for the ones removed before it).
func Sweep(ctx context.Context, q Queries, cutoff time.Time, batchSize int, pause time.Duration) (int64, error) {
	var total int64
	for {
		n, err := q.DeleteOldNodeUserUsages(ctx, generated.DeleteOldNodeUserUsagesParams{
			Cutoff:    pgtype.Timestamptz{Time: cutoff, Valid: true},
			BatchSize: int32(batchSize),
		})
		total += n
		if err != nil {
			return total, fmt.Errorf("delete old node_user_usages: %w", err)
		}
		if n < int64(batchSize) {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(pause):
		}
	}
}
