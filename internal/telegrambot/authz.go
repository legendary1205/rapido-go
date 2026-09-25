package telegrambot

import (
	"context"
	"slices"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// principal is the panel identity a Telegram account acts as. Every API call
// carries a token minted for exactly this admin.
type principal struct {
	Username string
	IsSudo   bool
}

// authorize maps a Telegram account to a panel identity, or reports that the
// account may not use the console at all.
//
//   - An id on the integration settings' admin list is sudo.
//   - Otherwise an admin whose own telegram_id matches acts with that admin's
//     privileges, so a reseller only ever reaches their own users.
//
// The account id, not the chat id, is what is matched, and only private chats
// reach here: a group id on the notification list must not let every member of
// that group run the panel.
func (c *Console) authorize(ctx context.Context, uid int64) (principal, bool) {
	vals, err := c.d.Settings(ctx)
	if err != nil {
		return principal{}, false
	}
	listed := slices.Contains(vals.TelegramAdminIDs, uid)

	owned, sudoFallback := c.adminsFor(ctx, uid)

	// A sudo admin bound to this Telegram account is the most specific identity.
	for _, a := range owned {
		if a.IsSudo {
			return principal{Username: a.Username, IsSudo: true}, true
		}
	}
	if listed {
		if c.d.SudoUsername != "" {
			return principal{Username: c.d.SudoUsername, IsSudo: true}, true
		}
		if sudoFallback != "" {
			return principal{Username: sudoFallback, IsSudo: true}, true
		}
		return principal{}, false
	}
	if len(owned) > 0 {
		return principal{Username: owned[0].Username}, true
	}
	return principal{}, false
}

// adminsFor returns the admins bound to a Telegram id plus the name of a sudo
// admin to stand in for listed ids when no env sudo account is configured. The
// admins table is read at most once per authTTL: a spammer must not turn every
// message into a query.
func (c *Console) adminsFor(ctx context.Context, uid int64) ([]generated.Admin, string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.authByID == nil || c.now().Sub(c.authAt) >= c.authTTL {
		admins, err := c.d.Queries.ListAdmins(ctx, generated.ListAdminsParams{})
		if err != nil {
			c.d.Logger.Warn("telegram console: could not read admins", "error", err)
			if c.authByID == nil {
				return nil, ""
			}
		} else {
			byID := map[int64][]generated.Admin{}
			var fallback, ownerFallback string
			for _, a := range admins {
				if a.TelegramID.Valid {
					byID[a.TelegramID.Int64] = append(byID[a.TelegramID.Int64], a)
				}
				if a.IsSudo && fallback == "" {
					fallback = a.Username
				}
				if a.IsOwner && a.IsSudo && ownerFallback == "" {
					ownerFallback = a.Username
				}
			}
			if ownerFallback != "" {
				fallback = ownerFallback
			}
			c.authByID = byID
			c.authAt = c.now()
			c.authSudo = fallback
		}
	}
	return c.authByID[uid], c.authSudo
}
