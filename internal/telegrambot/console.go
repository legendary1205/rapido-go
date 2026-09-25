package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
)

// Deps is everything the console needs from the panel process.
type Deps struct {
	Queries *generated.Queries
	Cache   *cache.Client
	Issuer  *auth.TokenIssuer
	// SudoUsername is the env-bootstrapped sudo account; chats on the Telegram
	// admin-id list act as it (or, when unset, as a sudo admin from the database).
	SudoUsername string
	Settings     func(ctx context.Context) (integrationsettings.Values, error)
	Logger       *slog.Logger
	// BaseURL overrides the Bot API host; tests point it at a fake server.
	BaseURL string
}

// Console is the Telegram admin console. It stays dormant until a bot token is
// configured and only ever polls from the process that runs Run - the backend
// singleton - because two pollers on one token fight each other.
type Console struct {
	d Deps

	apiMu sync.RWMutex
	api   http.Handler
	ready chan struct{}
	once  sync.Once

	// Tunables, fields rather than constants so tests can shrink them.
	sessionTTL     time.Duration
	settingsEvery  time.Duration
	pollTimeoutSec int
	authTTL        time.Duration
	refusalEvery   time.Duration
	pageSize       int
	limitBurst     float64
	limitPerSec    float64
	now            func() time.Time

	inflight sync.WaitGroup
	qmu      sync.Mutex
	queues   map[int64]*userQueue

	authMu   sync.Mutex
	authAt   time.Time
	authByID map[int64][]generated.Admin
	authSudo string

	limMu    sync.Mutex
	limiters map[int64]*bucket
	refusals map[int64]time.Time
}

func New(d Deps) *Console {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Console{
		d:              d,
		ready:          make(chan struct{}),
		sessionTTL:     20 * time.Minute,
		settingsEvery:  30 * time.Second,
		pollTimeoutSec: 25,
		authTTL:        30 * time.Second,
		refusalEvery:   10 * time.Minute,
		pageSize:       8,
		limitBurst:     10,
		limitPerSec:    2,
		now:            time.Now,
		queues:         map[int64]*userQueue{},
		limiters:       map[int64]*bucket{},
		refusals:       map[int64]time.Time{},
	}
}

// SetAPI hands the console the panel's router. It is called in-process, never
// through a listening socket, so the console can only reach what the panel's
// own handlers expose. Run waits for it, which lets the backend singleton start
// before the router is built.
func (c *Console) SetAPI(h http.Handler) {
	c.apiMu.Lock()
	c.api = h
	c.apiMu.Unlock()
	c.once.Do(func() { close(c.ready) })
}

func (c *Console) apiHandler() http.Handler {
	c.apiMu.RLock()
	defer c.apiMu.RUnlock()
	return c.api
}

// Run polls until ctx is cancelled. Settings are re-resolved every
// settingsEvery, so setting, changing or clearing the token takes effect
// without a restart.
func (c *Console) Run(ctx context.Context) {
	select {
	case <-c.ready:
	case <-ctx.Done():
		return
	}

	var (
		bot      *botClient
		botKey   string
		offset   int64
		backoff  time.Duration
		resolved time.Time
		lastWarn time.Time
	)
	warn := func(msg string, err error) {
		if time.Since(lastWarn) < time.Minute {
			return
		}
		lastWarn = time.Now()
		c.d.Logger.Warn(msg, "error", err)
	}

	for ctx.Err() == nil {
		if bot == nil || time.Since(resolved) >= c.settingsEvery {
			vals, err := c.d.Settings(ctx)
			if err != nil {
				warn("telegram console: could not resolve settings", err)
				sleepCtx(ctx, c.settingsEvery)
				continue
			}
			resolved = time.Now()
			if vals.TelegramAPIToken == "" {
				if bot != nil {
					c.d.Logger.Info("telegram console: token removed, going dormant")
					bot, botKey = nil, ""
				}
				sleepCtx(ctx, c.settingsEvery)
				continue
			}
			if key := vals.TelegramAPIToken + "\x00" + vals.TelegramProxyURL; key != botKey {
				bot = newBotClient(vals.TelegramAPIToken, c.d.BaseURL, vals.TelegramProxyURL)
				botKey, offset, backoff = key, 0, 0
				c.d.Logger.Info("telegram console: polling started")
			}
		}

		updates, err := bot.getUpdates(ctx, offset, c.pollTimeoutSec)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := pollBackoff(&backoff)
			var ae *apiError
			switch {
			case errors.As(err, &ae) && ae.Code == http.StatusConflict:
				warn("telegram console: another poller or a webhook holds this bot", err)
				wait = 30 * time.Second
			case errors.As(err, &ae) && (ae.Code == http.StatusUnauthorized || ae.Code == http.StatusNotFound):
				warn("telegram console: the bot token was rejected", err)
				wait = time.Minute
			case errors.As(err, &ae) && ae.RetryAfter > 0:
				wait = time.Duration(ae.RetryAfter) * time.Second
			default:
				warn("telegram console: getUpdates failed", err)
			}
			sleepCtx(ctx, wait)
			continue
		}
		backoff = 0
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			c.dispatch(ctx, bot, u)
		}
	}
}

// pollBackoff doubles from 1s up to 60s.
func pollBackoff(cur *time.Duration) time.Duration {
	if *cur == 0 {
		*cur = time.Second
	} else if *cur < time.Minute {
		*cur *= 2
	}
	if *cur > time.Minute {
		*cur = time.Minute
	}
	return *cur
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// userQueue serialises one admin's updates in arrival order without letting a
// slow action (a backup) stall anyone else or the polling loop.
type userQueue struct {
	mu      sync.Mutex
	pending []func()
	running bool
}

const maxQueued = 50

func (c *Console) enqueue(uid int64, fn func()) {
	c.qmu.Lock()
	q := c.queues[uid]
	if q == nil {
		q = &userQueue{}
		c.queues[uid] = q
	}
	c.qmu.Unlock()

	q.mu.Lock()
	if len(q.pending) >= maxQueued {
		q.mu.Unlock()
		return
	}
	q.pending = append(q.pending, fn)
	c.inflight.Add(1)
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()

	go func() {
		for {
			q.mu.Lock()
			if len(q.pending) == 0 {
				q.running = false
				q.mu.Unlock()
				return
			}
			next := q.pending[0]
			q.pending = q.pending[1:]
			q.mu.Unlock()
			func() {
				defer c.inflight.Done()
				defer func() {
					if p := recover(); p != nil {
						c.d.Logger.Error("telegram console: handler panicked", "panic", fmt.Sprint(p))
					}
				}()
				next()
			}()
		}
	}()
}

// bucket is a small token bucket: bursts of taps are fine, sustained floods are not.
type bucket struct {
	tokens float64
	at     time.Time
}

const maxTracked = 10000

func (c *Console) allow(uid int64) bool {
	c.limMu.Lock()
	defer c.limMu.Unlock()
	now := c.now()
	b := c.limiters[uid]
	if b == nil {
		if len(c.limiters) >= maxTracked {
			c.limiters = map[int64]*bucket{}
		}
		b = &bucket{tokens: c.limitBurst, at: now}
		c.limiters[uid] = b
	}
	b.tokens += now.Sub(b.at).Seconds() * c.limitPerSec
	if b.tokens > c.limitBurst {
		b.tokens = c.limitBurst
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// shouldRefuse reports whether an unauthorised chat is due its one refusal
// message; afterwards that chat is ignored until refusalEvery has passed.
func (c *Console) shouldRefuse(uid int64) bool {
	c.limMu.Lock()
	defer c.limMu.Unlock()
	now := c.now()
	if last, ok := c.refusals[uid]; ok && now.Sub(last) < c.refusalEvery {
		return false
	}
	if len(c.refusals) >= maxTracked {
		c.refusals = map[int64]time.Time{}
	}
	c.refusals[uid] = now
	return true
}
