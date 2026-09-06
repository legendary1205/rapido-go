// Package reviewjob ports app/jobs/review_users.py: the background loop
// that flips a user active->limited/expired when they cross their
// data_limit/expire, fires a pending NextPlan when one exists, and brings
// an on_hold user active once they've connected (or their hold timeout
// elapses). Only runs on the backend-role singleton - see
// internal/backendlock for how that's enforced.
//
// Deferred, and clearly so rather than silently: the current Python job
// also calls xray.operations.remove_user/update_user to drop the affected
// user's live connections on every node immediately - this Go rewrite has
// no panel-side registry yet of which nodes are running which users (that
// needs the node orchestration layer, a natural extension of Phase 3, not
// built in this phase). The DB-side status transition here is complete and
// correct on its own; a limited/expired user's existing connections simply
// keep working until the node's own config is next pushed, instead of
// being dropped the instant this job notices.
package reviewjob

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/report"
)

// Run ticks every interval until ctx is canceled, calling review once per
// tick. Errors are logged, not fatal - one bad tick shouldn't kill the
// whole backend process.
func Run(ctx context.Context, q *generated.Queries, dispatcher *report.Dispatcher, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := review(ctx, q, dispatcher, logger); err != nil {
				logger.Error("review_users tick failed", "error", err)
			}
		}
	}
}

func review(ctx context.Context, q *generated.Queries, dispatcher *report.Dispatcher, logger *slog.Logger) error {
	now := time.Now().UTC()

	if err := reviewActiveUsers(ctx, q, dispatcher, logger, now); err != nil {
		return err
	}
	return reviewOnHoldUsers(ctx, q, dispatcher, logger, now)
}

func reviewActiveUsers(ctx context.Context, q *generated.Queries, dispatcher *report.Dispatcher, logger *slog.Logger, now time.Time) error {
	users, err := q.GetUsersNeedingStatusReview(ctx, pgtype.Int4{Int32: int32(now.Unix()), Valid: true})
	if err != nil {
		return err
	}

	for _, user := range users {
		limited := user.DataLimit.Valid && user.DataLimit.Int64 > 0 && user.UsedTraffic >= user.DataLimit.Int64
		expired := user.Expire.Valid && user.Expire.Int32 != 0 && int64(user.Expire.Int32) <= now.Unix()

		if limited || expired {
			plan, err := q.GetNextPlanByUserID(ctx, user.ID)
			hasPlan := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if hasPlan && (plan.FireOnEither || (limited && expired)) {
				updated, err := fireNextPlan(ctx, q, user)
				if err != nil {
					return err
				}
				logger.Info("user data reset by next_plan", "username", user.Username)
				dispatcher.UserDataResetByNext(ctx, toUserSummary(updated), lookupAdminRef(ctx, q, updated.AdminID, logger))
				continue
			}
		}

		var status string
		switch {
		case limited:
			status = "limited"
		case expired:
			status = "expired"
		default:
			continue
		}
		if err := q.UpdateUserStatusOnly(ctx, generated.UpdateUserStatusOnlyParams{ID: user.ID, Status: status}); err != nil {
			return err
		}
		logger.Info("user status changed", "username", user.Username, "status", status)
		dispatcher.StatusChange(ctx, user.Username, status, lookupAdminRef(ctx, q, user.AdminID, logger))
	}
	return nil
}

// fireNextPlan mirrors crud.reset_user_by_next: log the pre-reset usage,
// clear per-node usage history, then apply the plan's formula in one
// UPDATE (see ResetUserByNextPlan's own comment on the
// add_remaining_traffic field's questionable naming - ported as-is).
// Returns the post-reset row - report.UserDataResetByNext needs the NEW
// data_limit/expire, matching Python's reset_user_by_next_report, which
// builds its UserResponse from the row only after the reset UPDATE runs.
func fireNextPlan(ctx context.Context, q *generated.Queries, user generated.User) (generated.User, error) {
	if err := q.CreateUserUsageLog(ctx, generated.CreateUserUsageLogParams{
		UserID: pgtype.Int4{Int32: user.ID, Valid: true}, UsedTrafficAtReset: user.UsedTraffic,
	}); err != nil {
		return generated.User{}, err
	}
	if err := q.ClearNodeUserUsages(ctx, pgtype.Int4{Int32: user.ID, Valid: true}); err != nil {
		return generated.User{}, err
	}
	updated, err := q.ResetUserByNextPlan(ctx, user.ID)
	if err != nil {
		return generated.User{}, err
	}
	// ResetUserByNextPlan only applies the plan's formula to the users row -
	// the plan itself is still there until deleted, matching the Python
	// original's explicit `await db.delete(dbuser.next_plan)`.
	if err := q.DeleteNextPlanByUserID(ctx, user.ID); err != nil {
		return generated.User{}, err
	}
	return updated, nil
}

func reviewOnHoldUsers(ctx context.Context, q *generated.Queries, dispatcher *report.Dispatcher, logger *slog.Logger, now time.Time) error {
	users, err := q.GetOnHoldUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		baseTime := user.CreatedAt.Time
		if user.EditAt.Valid {
			baseTime = user.EditAt.Time
		}

		activate := false
		switch {
		case user.OnlineAt.Valid && !baseTime.After(user.OnlineAt.Time):
			activate = true
		case user.OnHoldTimeout.Valid && !user.OnHoldTimeout.Time.After(now):
			activate = true
		}
		if !activate {
			continue
		}
		if err := q.ActivateOnHoldUser(ctx, user.ID); err != nil {
			return err
		}
		logger.Info("user status changed", "username", user.Username, "status", "active")
		dispatcher.StatusChange(ctx, user.Username, "active", lookupAdminRef(ctx, q, user.AdminID, logger))
	}
	return nil
}

// toUserSummary and lookupAdminRef are small, deliberately duplicated
// (rather than shared with internal/httpapi's equivalents) converters from
// generated.* rows to report's DB-free types - report.AdminRef/UserSummary
// exist specifically so neither this package nor httpapi needs to import
// the other just for a two-line struct conversion.
func toUserSummary(u generated.User) report.UserSummary {
	var dataLimit *int64
	if u.DataLimit.Valid {
		dataLimit = &u.DataLimit.Int64
	}
	var expire *int64
	if u.Expire.Valid && u.Expire.Int32 != 0 {
		v := int64(u.Expire.Int32)
		expire = &v
	}
	return report.UserSummary{
		Username: u.Username, DataLimit: dataLimit, Expire: expire,
		DataLimitResetStrategy: u.DataLimitResetStrategy,
	}
}

// lookupAdminRef resolves the owning admin for a report call - uncached
// (unlike internal/httpapi's CachedGetAdminByID), since this job runs at
// most once per affected user per tick, not once per HTTP request.
func lookupAdminRef(ctx context.Context, q *generated.Queries, adminID pgtype.Int4, logger *slog.Logger) *report.AdminRef {
	if !adminID.Valid {
		return nil
	}
	admin, err := q.GetAdminByID(ctx, adminID.Int32)
	if err != nil {
		logger.Warn("reviewjob: could not resolve owning admin for report", "admin_id", adminID.Int32, "error", err)
		return nil
	}
	ref := report.AdminRef{Username: admin.Username}
	if admin.TelegramID.Valid {
		ref.TelegramID = &admin.TelegramID.Int64
	}
	if admin.DiscordWebhook.Valid {
		ref.DiscordWebhook = &admin.DiscordWebhook.String
	}
	return &ref
}
