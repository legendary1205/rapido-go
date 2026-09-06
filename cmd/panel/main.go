// Command panel is the rapido-go entrypoint. It runs in one of two roles,
// set via the ROLE env var - "api" (stateless, horizontally replicated) or
// "backend" (the singleton owning node connections, core lifecycle and
// schedulers) - mirroring the current Python panel's process model. Phase 0
// and 1 only implement the api role's HTTP surface; the backend role's
// node/core responsibilities land in later phases.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/certs"
	"github.com/legendary1205/rapido-go/internal/config"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/httpapi"
	"github.com/legendary1205/rapido-go/internal/reviewjob"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger.Info("starting", "role", cfg.Role)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	logger.Info("connected to postgres")

	redisClient := cache.New(cfg.RedisAddr, cfg.RedisPass, cfg.RedisDB)
	defer redisClient.Close()
	if err := redisClient.Ping(ctx); err != nil {
		return err
	}
	logger.Info("connected to redis")

	queries := generated.New(pool)

	secret, err := ensureJWTSecret(ctx, queries)
	if err != nil {
		return err
	}
	issuer := auth.NewTokenIssuer(secret, cfg.JWTAccessTTL)

	if err := ensureTLS(ctx, queries, logger); err != nil {
		return err
	}

	if cfg.Role == config.RoleBackend {
		go runAsBackendSingleton(ctx, cfg.DatabaseURL, queries, logger)
	}

	store := httpapi.NewStore(pool)
	handler := httpapi.NewHandler(store, issuer, cfg.SudoUsername, cfg.SudoPassword, secret, cfg.PublicIP, cfg.SubscriptionURLPrefix, logger)
	router := httpapi.NewRouter(handler, logger, cfg.AllowedOrigins)

	srv := &http.Server{
		Addr:              cfg.HTTPHost + ":" + strconv.Itoa(cfg.HTTPPort),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logger.Info("shutting down")
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown error", "error", err)
		}
	}()

	logger.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runAsBackendSingleton holds a session-scoped Postgres advisory lock for
// as long as this process is the active backend singleton - the direct
// Postgres equivalent of the current system's MySQL GET_LOCK() usage,
// which exists because a duplicate BACKEND role process double-driving the
// same background jobs (and, in later phases, the same node connections)
// caused a real production outage once. Retries on an interval if another
// backend process already holds the lock, so a second instance started by
// mistake (or during a rolling restart) waits rather than running
// alongside the first one.
//
// Uses a single dedicated connection opened directly with pgx, not one
// borrowed from the pgxpool: a session-level advisory lock lives exactly
// as long as its holding connection does, and a pool connection can be
// silently recycled or closed at any time, which would release the lock
// out from under this process without it noticing.
func runAsBackendSingleton(ctx context.Context, databaseURL string, queries *generated.Queries, logger *slog.Logger) {
	const retryInterval = 10 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := pgx.Connect(ctx, databaseURL)
		if err != nil {
			logger.Error("backend singleton: connect for advisory lock", "error", err)
			time.Sleep(retryInterval)
			continue
		}

		var acquired bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", cache.AdvisoryLockBackendSingleton).Scan(&acquired); err != nil {
			logger.Error("backend singleton: pg_try_advisory_lock", "error", err)
			conn.Close(ctx)
			time.Sleep(retryInterval)
			continue
		}
		if !acquired {
			logger.Info("backend singleton: lock held elsewhere, waiting", "retry_in", retryInterval)
			conn.Close(ctx)
			time.Sleep(retryInterval)
			continue
		}

		logger.Info("backend singleton: acquired advisory lock, running background jobs")
		reviewjob.Run(ctx, queries, logger, 10*time.Second)

		// reviewjob.Run only returns once ctx is canceled (process shutdown) -
		// release the lock and let the deferred loop exit via ctx.Done() above.
		conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", cache.AdvisoryLockBackendSingleton)
		conn.Close(context.Background())
		return
	}
}

// ensureJWTSecret returns the singleton jwt_secrets row's key, generating
// and persisting one on first boot - equivalent to app/utils/jwt.py's
// get_secret_key, which lazily creates the "jwt" table's one row the same
// way via the SQLAlchemy column default.
func ensureJWTSecret(ctx context.Context, q *generated.Queries) ([]byte, error) {
	existing, err := q.GetJWTSecret(ctx)
	if err == nil {
		return []byte(existing.SecretKey), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	secretHex := hex.EncodeToString(raw)
	created, err := q.CreateJWTSecret(ctx, secretHex)
	if err != nil {
		return nil, err
	}
	return []byte(created.SecretKey), nil
}

// ensureTLS returns the singleton tls row, generating the Rapido-branded
// self-signed CA/panel identity on first boot if none exists yet.
func ensureTLS(ctx context.Context, q *generated.Queries, logger *slog.Logger) error {
	_, err := q.GetTLS(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	pair, _, err := certs.GenerateCA()
	if err != nil {
		return err
	}
	if _, err := q.CreateTLS(ctx, generated.CreateTLSParams{Key: pair.KeyPEM, Certificate: pair.CertPEM}); err != nil {
		return err
	}
	logger.Info("generated Rapido CA/panel certificate", "cn", certs.CommonName)
	return nil
}
