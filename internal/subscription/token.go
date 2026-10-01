// Package subscription generates the per-user subscription link content
// (v2ray share links, sing-box/clash/outline configs) that VPN client apps
// import - the Go port of app/subscription/*.py.
package subscription

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

// CreateToken mirrors app/utils/jwt.py's create_subscription_token exactly:
// base64url(no padding) of "<username>,<unix_ts>", followed by the first 10
// characters of base64url(sha256(that string + secret)). Not a JWT - a
// lightweight signed token with no expiry (validity is instead bounded by
// comparing its embedded timestamp against the user's created_at/
// sub_revoked_at at verification time - see ParseToken's caller).
//
// The embedded timestamp is rounded UP to the next whole second (matching
// Python's ceil(time.time()), not a plain truncating Unix()) - a token
// minted milliseconds after a user's Postgres created_at (sub-second
// precision) would otherwise floor to the same or an earlier whole second,
// making created_at.After(tokenTime) true and permanently 404 a
// subscription URL that was valid the instant it was issued.
func CreateToken(username string, secret []byte) string {
	now := time.Now().UTC()
	ts := now.Unix()
	if now.Nanosecond() > 0 {
		ts++
	}
	data := fmt.Sprintf("%s,%d", username, ts)
	dataB64 := base64.RawURLEncoding.EncodeToString([]byte(data))
	return dataB64 + sign(dataB64, secret)
}

// TokenClaims is who a subscription token names and when it was minted.
// ByID tokens (PasarGuard's "v2,<id>,<ts>"/"v3,<id>,<ts>" payloads) name the
// user by numeric id and leave Username empty; every other format names
// the user by username.
type TokenClaims struct {
	ByID      bool
	UserID    int64
	Username  string
	CreatedAt time.Time
}

// jwtSubscriptionPrefix is base64url(`{"alg":"HS256","typ":"JWT"}`) plus the
// separator - the exact header every Marzban-lineage panel's old JWT
// subscription links start with, and the same literal check PasarGuard's
// get_subscription_payload uses to route a token to its JWT branch.
const jwtSubscriptionPrefix = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9."

// ParseToken verifies a subscription token and returns what it names. It
// accepts every format PasarGuard 5.x's get_subscription_payload accepts
// (app/utils/jwt.py), because a migrated panel's customers keep whatever
// link they were given - one this panel refuses is a customer whose app
// silently stops updating:
//
//   - this panel's own (and Marzban's) "<username>,<ts>" + 10-character
//     signature, where the signature is the base64url OR the hex prefix of
//     sha256(payload + secret) - older releases issued the hex form;
//   - the same 10-character shape around "v2,<user_id>,<ts>";
//   - PasarGuard's current "<b64 payload>.<b64 HMAC-SHA256>", whose payload
//     is normally "v3,<user_id>,<ts>" - the format every PasarGuard 5.x
//     panel hands out today;
//   - the legacy HS256 JWT with access=subscription.
//
// secret is the panel's signing key as raw bytes; every one of these
// formats keys on its 64-character hex TEXT (see sign's doc comment), so
// that is what each branch reconstructs. Signatures are compared in
// constant time.
func ParseToken(token string, secret []byte) (TokenClaims, bool) {
	if len(token) < 15 {
		return TokenClaims{}, false
	}
	key := hex.EncodeToString(secret)

	switch {
	case strings.HasPrefix(token, jwtSubscriptionPrefix):
		return parseJWTToken(token, key)

	case strings.Contains(token, "."):
		cut := strings.LastIndexByte(token, '.')
		dataB64, signature := token[:cut], token[cut+1:]
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(dataB64))
		expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(signature), []byte(expected)) {
			return TokenClaims{}, false
		}
		return parseTokenPayload(dataB64)

	default:
		dataB64, signature := token[:len(token)-10], token[len(token)-10:]
		if subtle.ConstantTimeCompare([]byte(signature), []byte(sign(dataB64, secret))) != 1 &&
			subtle.ConstantTimeCompare([]byte(signature), []byte(signHex(dataB64, secret))) != 1 {
			return TokenClaims{}, false
		}
		return parseTokenPayload(dataB64)
	}
}

// parseTokenPayload decodes the signed payload shared by the 10-character
// and HMAC formats - _parse_subscription_data's two shapes, nothing else.
func parseTokenPayload(dataB64 string) (TokenClaims, bool) {
	raw, ok := decodeTokenB64(dataB64)
	if !ok {
		return TokenClaims{}, false
	}
	parts := strings.Split(raw, ",")
	switch {
	case len(parts) == 3 && (parts[0] == "v2" || parts[0] == "v3"):
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return TokenClaims{}, false
		}
		ts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return TokenClaims{}, false
		}
		return TokenClaims{ByID: true, UserID: id, CreatedAt: time.Unix(ts, 0).UTC()}, true
	case len(parts) == 2:
		ts, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return TokenClaims{}, false
		}
		return TokenClaims{Username: parts[0], CreatedAt: time.Unix(ts, 0).UTC()}, true
	}
	return TokenClaims{}, false
}

// decodeTokenB64 mirrors _decode_b64_token: Python decodes with
// altchars="-_" after translating them to "+/", so a payload in either
// base64 alphabet decodes, padded or not; anything that is not valid UTF-8
// is rejected.
func decodeTokenB64(s string) (string, bool) {
	s = strings.NewReplacer("+", "-", "/", "_").Replace(strings.TrimRight(s, "="))
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

// parseJWTToken accepts the old JWT subscription link: HS256 over the
// secret's hex text, access=subscription, a non-empty sub, an iat. An exp,
// when present, is enforced (PyJWT's decode does the same).
func parseJWTToken(token, key string) (TokenClaims, bool) {
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return []byte(key), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return TokenClaims{}, false
	}
	if access, _ := claims["access"].(string); access != "subscription" {
		return TokenClaims{}, false
	}
	username, _ := claims["sub"].(string)
	if username == "" {
		return TokenClaims{}, false
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		return TokenClaims{}, false
	}
	sec, frac := math.Modf(iat)
	return TokenClaims{Username: username, CreatedAt: time.Unix(int64(sec), int64(frac*1e9)).UTC()}, true
}

// sign hashes dataB64 with secret rendered as its HEX-STRING TEXT, not its
// raw bytes - a real, previously-undetected porting mismatch. Python's
// original does `sha256((data_b64_str + get_secret_key()).encode("utf-8"))`,
// and get_secret_key() returns the jwt_secrets/`jwt` table's secret_key
// column as-is: a 64-character hex STRING, concatenated as literal text.
// This package's caller (cmd/panel/main.go's ensureJWTSecret) instead
// hex-decodes that same column into 32 raw bytes before handing it out as
// `secret` here - every other use of that value (there is currently only
// this one) has no reason to care, so nothing surfaced the mismatch until
// a real cross-system migration needed an old, Python-issued subscription
// token to verify against this Go binary: every single previously-issued
// link failed, indistinguishable from a wrong secret value, even once the
// secret's VALUE was correctly carried over from the old panel. Re-hex-
// encoding here reconstructs the exact 64-character string Python signs
// with, regardless of what form the caller's own copy of the secret takes.
func sign(dataB64 string, secret []byte) string {
	h := sha256.Sum256([]byte(dataB64 + hex.EncodeToString(secret)))
	full := base64.URLEncoding.EncodeToString(h[:])
	return full[:10]
}

// signHex is sign's older sibling: the same digest as lowercase hex, cut to
// 10 characters. Releases before the base64 form issued these, and
// PasarGuard still accepts them (u_token_hex_resign).
func signHex(dataB64 string, secret []byte) string {
	h := sha256.Sum256([]byte(dataB64 + hex.EncodeToString(secret)))
	return hex.EncodeToString(h[:])[:10]
}
