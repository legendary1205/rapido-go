package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const contextIdentityKey = "auth.identity"

// ContextAttemptedUsernameKey / ContextAuthErrorKey carry who a *rejected*
// request claimed to be and why it was rejected, so the API-client log
// (internal/httpapi/apiclientlog.go) can name the account even though the
// request never produced an Identity. Nothing else reads them.
const (
	ContextAttemptedUsernameKey = "auth.attempted_username"
	ContextAuthErrorKey         = "auth.error"
)

// Identity is the resolved admin identity attached to the request context
// once a middleware below has run. AdminID is 0 for the env-bootstrapped
// sudo account, which has no admins row at all.
type Identity struct {
	AdminID  int32
	Username string
	IsSudo   bool
	// IsOwner is the one tier above sudo - can grant/revoke sudo (including
	// on other sudo admins) and appoint/remove other owners. The
	// env-bootstrapped sudo account is always treated as owner too: it is
	// the only way into a panel with zero admin rows, so it needs the
	// authority to create the very first real owner.
	IsOwner bool
}

// Resolver looks up a DB-backed admin's id, current sudo/owner flags and
// password-reset timestamp by username. Implemented by internal/httpapi's
// admin store.
type Resolver interface {
	ResolveAdmin(ctx context.Context, username string) (adminID int32, isSudo bool, isOwner bool, passwordResetAt *time.Time, found bool, err error)
}

// RequireAdmin accepts any valid, still-live token - equivalent to the
// current Admin.get_current FastAPI dependency (app/models/admin.py's
// Admin.get_admin classmethod). Two special cases mirror that code exactly:
//
//   - A token for the env-bootstrapped sudo username is trusted outright,
//     with no DB lookup at all - that account need not have a DB row.
//   - For every other token, the *current* admins row is the source of
//     truth for is_sudo (not the token's own "access" claim), and a token
//     issued before the admin's last password reset is rejected even if it
//     hasn't expired yet - so changing a password invalidates old tokens.
func RequireAdmin(issuer *TokenIssuer, resolver Resolver, sudoUsername string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, reason, err := resolve(c, issuer, resolver, sudoUsername)
		if err != nil {
			unauthorized(c, reason)
			return
		}
		c.Set(contextIdentityKey, identity)
		c.Next()
	}
}

// RequireSudo additionally rejects a valid but non-sudo identity -
// equivalent to Admin.check_sudo_admin.
func RequireSudo(issuer *TokenIssuer, resolver Resolver, sudoUsername string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, reason, err := resolve(c, issuer, resolver, sudoUsername)
		if err != nil {
			unauthorized(c, reason)
			return
		}
		if !identity.IsSudo {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"detail": "You're not allowed"})
			return
		}
		c.Set(contextIdentityKey, identity)
		c.Next()
	}
}

// Rejection reasons, surfaced on the 401 as an X-Auth-Error header. The
// response body stays byte-identical to the real panel's
// {"detail":"Could not validate credentials"} - clients parse that and must
// not see anything new - but "why" is otherwise completely invisible from
// outside, which turns any misconfigured client into the same opaque
// symptom. A header costs nothing and is visible in the reverse proxy's
// own access log.
const (
	reasonNoBearer      = "no-bearer-token"
	reasonBadToken      = "invalid-or-expired-token"
	reasonUnknownAdmin  = "token-admin-no-longer-exists"
	reasonPasswordReset = "token-predates-password-change"
)

func resolve(c *gin.Context, issuer *TokenIssuer, resolver Resolver, sudoUsername string) (*Identity, string, error) {
	header := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return nil, reasonNoBearer, ErrInvalidToken
	}
	claims, err := issuer.Verify(strings.TrimPrefix(header, prefix))
	if err != nil {
		return nil, reasonBadToken, err
	}
	// Recorded even on the paths that go on to reject: the API-client log
	// is far more useful when it can name the account a failing bot is
	// trying to be.
	c.Set(ContextAttemptedUsernameKey, claims.Username)

	if claims.IsSudo() && sudoUsername != "" && claims.Username == sudoUsername {
		return &Identity{Username: claims.Username, IsSudo: true, IsOwner: true}, "", nil
	}

	adminID, isSudo, isOwner, passwordResetAt, found, err := resolver.ResolveAdmin(c.Request.Context(), claims.Username)
	if err != nil || !found {
		return nil, reasonUnknownAdmin, ErrInvalidToken
	}
	if passwordResetAt != nil {
		if claims.IssuedAt == nil || passwordResetAt.After(claims.IssuedAt.Time) {
			return nil, reasonPasswordReset, ErrInvalidToken
		}
	}
	return &Identity{AdminID: adminID, Username: claims.Username, IsSudo: isSudo, IsOwner: isOwner}, "", nil
}

func unauthorized(c *gin.Context, reason string) {
	c.Header("WWW-Authenticate", "Bearer")
	if reason != "" {
		c.Header("X-Auth-Error", reason)
		c.Set(ContextAuthErrorKey, reason)
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Could not validate credentials"})
}

// CurrentIdentity fetches the identity RequireAdmin/RequireSudo attached to
// the request context. Only call this after one of those middlewares has run.
func CurrentIdentity(c *gin.Context) *Identity {
	v, ok := c.Get(contextIdentityKey)
	if !ok {
		return nil
	}
	return v.(*Identity)
}
