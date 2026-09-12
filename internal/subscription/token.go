// Package subscription generates the per-user subscription link content
// (v2ray share links, sing-box/clash/outline configs) that VPN client apps
// import - the Go port of app/subscription/*.py.
package subscription

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CreateToken mirrors app/utils/jwt.py's create_subscription_token exactly:
// base64url(no padding) of "<username>,<unix_ts>", followed by the first 10
// characters of base64url(sha256(that string + secret)). Not a JWT - a
// lightweight signed token with no expiry (validity is instead bounded by
// comparing its embedded timestamp against the user's created_at/
// sub_revoked_at at verification time - see ValidateToken's caller).
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

// ValidateToken verifies the signature and returns the embedded username
// and creation time. Unlike the Python original's plain string comparison,
// this uses a constant-time comparison for the signature - a strictly
// stronger check of the same wire format, not a format change.
func ValidateToken(token string, secret []byte) (username string, createdAt time.Time, ok bool) {
	if len(token) < 15 {
		return "", time.Time{}, false
	}
	dataB64, signature := token[:len(token)-10], token[len(token)-10:]
	expected := sign(dataB64, secret)
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
		return "", time.Time{}, false
	}

	raw, err := base64.RawURLEncoding.DecodeString(dataB64)
	if err != nil {
		return "", time.Time{}, false
	}
	parts := strings.SplitN(string(raw), ",", 2)
	if len(parts) != 2 {
		return "", time.Time{}, false
	}
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return parts[0], time.Unix(ts, 0).UTC(), true
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
