package hostmetrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// MetricsRetention mirrors METRICS_RETENTION_HOURS - samples are only
// useful while the chart still shows them.
const MetricsRetention = 48 * time.Hour

// PruneLoop deletes host_metrics rows older than MetricsRetention on
// interval, until ctx is canceled - the maintenance half of the push
// model's job list (see collect_metrics.py's own prune_metrics, run
// hourly). Meant to run as a goroutine inside the backend singleton's
// locked section, alongside reviewjob.Run.
func PruneLoop(ctx context.Context, queries *generated.Queries, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().UTC().Add(-MetricsRetention)
			deleted, err := queries.PruneOldHostMetrics(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
			if err != nil {
				logger.Error("hostmetrics: prune", "error", err)
				continue
			}
			if deleted > 0 {
				logger.Info("hostmetrics: pruned old samples", "count", deleted)
			}
		}
	}
}

// PanelSelfSampleLoop samples the panel's own machine on interval and
// stores it as a node_id-NULL host_metrics row - the Go equivalent of
// collect_metrics.py's own _panel_sample, reusing Collect/Store instead of
// psutil since this package already reads the same /proc files directly.
// Meant to run as a goroutine inside the backend singleton's locked
// section: sampling the panel from every stateless API replica too would
// write one redundant row per replica per tick.
func PanelSelfSampleLoop(ctx context.Context, queries *generated.Queries, tracker *PreviousTracker, logger *slog.Logger, interval time.Duration) {
	const selfKey = "panel"
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample := Collect(false, "")
			derived := tracker.Derive(selfKey, sample)
			if err := Store(ctx, queries, nil, sample, derived, true); err != nil {
				logger.Error("hostmetrics: store panel self-sample", "error", err)
			}
		}
	}
}
