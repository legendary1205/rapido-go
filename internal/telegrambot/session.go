package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Keys live in Redis, not in memory, so a restart or a second backend does not
// drop a half-finished wizard.
func sessionKey(uid int64) string { return fmt.Sprintf("rapido:tgbot:sess:%d", uid) }
func langKey(uid int64) string    { return fmt.Sprintf("rapido:tgbot:lang:%d", uid) }
func updateKey(id int64) string   { return fmt.Sprintf("rapido:tgbot:upd:%d", id) }

const langTTL = 400 * 24 * time.Hour

// Modes of a session: what the next plain text message means.
const (
	modeWizard = "nu"   // new-user wizard, see Step
	modeExtend = "exp"  // custom number of days to add to User
	modeData   = "dat"  // custom number of GB to add to User
	modeNote   = "note" // replacement note for User
)

// Steps of the new-user wizard.
const (
	stepName    = "name"
	stepTmpl    = "tmpl"
	stepLimit   = "limit"
	stepDays    = "days"
	stepConfirm = "confirm"
)

type draft struct {
	Name       string `json:"n,omitempty"`
	HasTmpl    bool   `json:"ht,omitempty"` // the panel has templates to offer
	TmplID     int32  `json:"ti,omitempty"`
	TmplName   string `json:"tn,omitempty"`
	Prefix     string `json:"pf,omitempty"`
	Suffix     string `json:"sf,omitempty"`
	LimitBytes int64  `json:"l,omitempty"`
	ExpireSecs int64  `json:"e,omitempty"` // lifetime from creation; 0 = never
	// Inbounds carries a template's protocol -> tags choice through to creation.
	Inbounds map[string][]string `json:"ib,omitempty"`
}

type session struct {
	Mode  string `json:"m,omitempty"`
	Step  string `json:"s,omitempty"`
	User  string `json:"u,omitempty"`
	Msg   int    `json:"g,omitempty"` // console message edited in place
	Draft draft  `json:"d"`
}

func (c *Console) loadSession(ctx context.Context, uid int64) (*session, bool) {
	raw, err := c.d.Cache.Get(ctx, sessionKey(uid))
	if err != nil {
		return nil, false
	}
	var s session
	if json.Unmarshal([]byte(raw), &s) != nil || s.Mode == "" {
		return nil, false
	}
	return &s, true
}

func (c *Console) saveSession(ctx context.Context, uid int64, s *session) {
	raw, err := json.Marshal(s)
	if err != nil {
		return
	}
	if err := c.d.Cache.Set(ctx, sessionKey(uid), string(raw), c.sessionTTL); err != nil {
		c.d.Logger.Warn("telegram console: could not save session", "error", err)
	}
}

func (c *Console) clearSession(ctx context.Context, uid int64) {
	_ = c.d.Cache.Del(ctx, sessionKey(uid))
}

// firstDelivery marks an update as seen; false means it was already handled,
// which happens when a restart makes Telegram redeliver the last batch.
func (c *Console) firstDelivery(ctx context.Context, updateID int64) bool {
	ok, err := c.d.Cache.Raw().SetNX(ctx, updateKey(updateID), "1", 24*time.Hour).Result()
	if err != nil {
		return true // better a rare duplicate than a dead console while Redis hiccups
	}
	return ok
}

type lang int

const (
	langFA lang = iota
	langEN
)

// langFromCode is Persian for Persian speakers and for anyone whose client does
// not say; every other language gets English.
func langFromCode(code string) lang {
	code = strings.ToLower(code)
	if code == "" || strings.HasPrefix(code, "fa") {
		return langFA
	}
	return langEN
}

func (c *Console) loadLang(ctx context.Context, uid int64, code string) lang {
	if v, err := c.d.Cache.Get(ctx, langKey(uid)); err == nil {
		switch v {
		case "fa":
			return langFA
		case "en":
			return langEN
		}
	}
	return langFromCode(code)
}

func (c *Console) saveLang(ctx context.Context, uid int64, l lang) {
	v := "fa"
	if l == langEN {
		v = "en"
	}
	_ = c.d.Cache.Set(ctx, langKey(uid), v, langTTL)
}
