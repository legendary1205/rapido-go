// Package resellerusagejob is the BACKEND-role-only loop that reports each
// admin's traffic to the reseller bot, which bills the reseller's wallet
// from it - the port of the Python panel's ResellerAPI.report_admin_usage.
// Mirrors internal/reviewjob's Run(ctx, ..., interval) shape.
//
// Node reports queue the traffic (BulkIncrementAdminUsage, migration 00014)
// only while the integration is configured, the same as the Python panel,
// which never accumulated usage for a bot it did not have. This loop sends
// whatever is queued and moves it to `reported` only once the bot has
// accepted it, so an unreachable bot delays billing without losing any.
//
// The bot charges as soon as it has processed a report, so an answer lost
// after that point (a timeout) means the next tick sends the same traffic
// again. That is why the reporting HTTP client gets a generous timeout
// rather than the 3s the per-request lookups use.
package resellerusagejob

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/resellerapi"
)

// Queries is the part of generated.Querier this job uses.
type Queries interface {
	GetPendingResellerAPIUsage(ctx context.Context) ([]generated.GetPendingResellerAPIUsageRow, error)
	MarkResellerAPIUsageReported(ctx context.Context, arg generated.MarkResellerAPIUsageReportedParams) error
}

// Reporter delivers one usage report; *resellerapi.Client satisfies it.
type Reporter interface {
	ReportUsages(ctx context.Context, cfg resellerapi.Config, usages []resellerapi.Usage) error
}

// SettingsFunc resolves the integration settings (DB override over env).
type SettingsFunc func(ctx context.Context) (integrationsettings.Values, error)

// Run ticks every interval until ctx is canceled. Like reviewjob, it skips
// ticks while maintenance mode is on - a restore may have the schema half
// rebuilt for a moment.
func Run(ctx context.Context, q Queries, reporter Reporter, settings SettingsFunc, maintenance *cache.Client, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if on, err := maintenance.IsMaintenanceMode(ctx); err != nil {
				logger.Error("reseller usage report: checking maintenance mode", "error", err)
				continue
			} else if on {
				continue
			}
			if err := Tick(ctx, q, reporter, settings); err != nil {
				logger.Error("reseller usage report failed, usage stays queued", "error", err)
			}
		}
	}
}

// Tick sends everything queued in one report. It is a no-op while the
// integration is not configured, and returns an error - leaving the queue
// untouched - when the report could not be delivered.
func Tick(ctx context.Context, q Queries, reporter Reporter, settings SettingsFunc) error {
	vals, err := settings(ctx)
	if err != nil {
		return fmt.Errorf("resolve integration settings: %w", err)
	}
	cfg := resellerapi.Config{Secret: vals.ResellerApiSecret, URL: vals.ResellerApiUrl}
	if !cfg.CanReportUsage() {
		return nil
	}

	rows, err := q.GetPendingResellerAPIUsage(ctx)
	if err != nil {
		return fmt.Errorf("load queued usage: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	usages := make([]resellerapi.Usage, len(rows))
	adminIDs := make([]int32, len(rows))
	sent := make([]int64, len(rows))
	var total int64
	for i, row := range rows {
		usages[i] = resellerapi.Usage{Username: row.Username, Usage: row.Pending}
		adminIDs[i] = row.AdminID
		sent[i] = row.Pending
		total += row.Pending
	}

	if err := reporter.ReportUsages(ctx, cfg, usages); err != nil {
		return fmt.Errorf("send %d bytes for %d admins: %w", total, len(rows), err)
	}

	// The bot has charged for this traffic by now, so marking it must not
	// be skipped just because shutdown started a moment ago - an unmarked
	// report is sent, and charged, a second time.
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := q.MarkResellerAPIUsageReported(markCtx, generated.MarkResellerAPIUsageReportedParams{
		AdminIds: adminIDs, Sent: sent,
	}); err != nil {
		return fmt.Errorf("reported %d bytes but could not mark them sent, they will be sent again: %w", total, err)
	}
	return nil
}
