package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// BenchmarkBuildNodeConfigBody reproduces the production fleet's actual
// shape - ~10k active users and 15 vless inbounds - because that shape, not
// the user count alone, is what makes this endpoint expensive: every inbound
// carries the WHOLE user list, so the response repeats those 10k credentials
// 15 times (~14 MB on the real panel).
//
//	go test ./internal/httpapi -run '^$' -bench BuildNodeConfigBody -cpuprofile cpu.out
//	go tool pprof -top -nodecount=20 cpu.out
const (
	benchNodeConfigUsers    = 10000
	benchNodeConfigInbounds = 15
)

func seedNodeConfigFleet(b *testing.B, h *Handler) {
	b.Helper()
	ctx := context.Background()
	q := h.store.Queries

	for i := 0; i < benchNodeConfigInbounds; i++ {
		tag := fmt.Sprintf("VLESS_TCP_%02d", i)
		if _, err := q.UpsertInbound(ctx, generated.UpsertInboundParams{
			Tag: tag, Protocol: "vless", Network: "tcp", Security: "none",
		}); err != nil {
			b.Fatalf("UpsertInbound(%s): %v", tag, err)
		}
		if _, err := q.CreateHost(ctx, generated.CreateHostParams{
			InboundTag: tag, Remark: fmt.Sprintf("n%02d", i), Address: "1.2.3.4",
			Port: pgInt4FromInt(8443 + i), Security: "inbound_default", Alpn: "none", Fingerprint: "none",
		}); err != nil {
			b.Fatalf("CreateHost(%s): %v", tag, err)
		}
	}

	for i := 0; i < benchNodeConfigUsers; i++ {
		u, err := q.CreateUser(ctx, generated.CreateUserParams{
			Username:               fmt.Sprintf("fleetuser%05d", i),
			Status:                 statusActive,
			DataLimit:              pgInt8FromInt64(50 << 30),
			Expire:                 pgInt4FromInt(2000000000),
			DataLimitResetStrategy: "no_reset",
		})
		if err != nil {
			b.Fatalf("CreateUser(%d): %v", i, err)
		}
		if _, err := q.CreateProxy(ctx, generated.CreateProxyParams{
			UserID:   pgInt4FromInt(int(u.ID)),
			Type:     "vless",
			Settings: []byte(`{"id":"3f1b8c2e-1d4a-4f6b-9c2e-8a7d6b5c4e3f","flow":""}`),
		}); err != nil {
			b.Fatalf("CreateProxy(%d): %v", i, err)
		}
	}
}

func BenchmarkBuildNodeConfigBody(b *testing.B) {
	_, _, handler := newTestRouterAndHandler(b)
	seedNodeConfigFleet(b, handler)
	ctx := context.Background()

	body, err := handler.buildNodeConfigBody(ctx, nodeProfile{})
	if err != nil {
		b.Fatalf("buildNodeConfigBody: %v", err)
	}
	b.Logf("node-config body is %.2f MB", float64(len(body))/(1024*1024))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := handler.buildNodeConfigBody(ctx, nodeProfile{}); err != nil {
			b.Fatalf("buildNodeConfigBody: %v", err)
		}
	}
}

// seedNodeConfigFleetBulk is seedNodeConfigFleet with the users inserted in
// two set-based statements, which is the difference between seconds and
// half an hour when the database is reached over a tunnel.
func seedNodeConfigFleetBulk(b *testing.B, h *Handler, users int) {
	b.Helper()
	ctx := context.Background()
	q := h.store.Queries
	for i := 0; i < benchNodeConfigInbounds; i++ {
		tag := fmt.Sprintf("VLESS_TCP_%02d", i)
		if _, err := q.UpsertInbound(ctx, generated.UpsertInboundParams{Tag: tag, Protocol: "vless", Network: "tcp", Security: "none"}); err != nil {
			b.Fatalf("UpsertInbound(%s): %v", tag, err)
		}
		if _, err := q.CreateHost(ctx, generated.CreateHostParams{
			InboundTag: tag, Remark: fmt.Sprintf("n%02d", i), Address: "1.2.3.4",
			Port: pgInt4FromInt(8443 + i), Security: "inbound_default", Alpn: "none", Fingerprint: "none",
		}); err != nil {
			b.Fatalf("CreateHost(%s): %v", tag, err)
		}
	}
	pool := h.store.Pool
	if _, err := pool.Exec(ctx, `INSERT INTO users (username, status, data_limit_reset_strategy)
		SELECT 'fleetuser' || lpad(g::text, 5, '0'), 'active', 'no_reset' FROM generate_series(1, $1::int) g`, users); err != nil {
		b.Fatalf("insert users: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO proxies (user_id, type, settings)
		SELECT id, 'vless', '{"id":"3f1b8c2e-1d4a-4f6b-9c2e-8a7d6b5c4e3f","flow":""}'::jsonb FROM users`); err != nil {
		b.Fatalf("insert proxies: %v", err)
	}
}

// BenchmarkNodeConfigPoll is what one node's poll costs at the production
// fleet's shape, in the three situations that matter: the data changed (a
// full rebuild), nothing changed (served from memory after one version
// query), and nothing changed for a node that sends If-None-Match (an empty
// 304). The number of snapshot loads is reported so "does not rebuild" is
// measured rather than assumed.
//
//	go test ./internal/httpapi -run '^$' -bench NodeConfigPoll -benchtime 30x
func BenchmarkNodeConfigPoll(b *testing.B) {
	router, _, handler := newTestRouterAndHandler(b)
	seedNodeConfigFleetBulk(b, handler, benchNodeConfigUsers)
	ctx := context.Background()

	const secret = "bench-node-secret"
	if _, err := handler.store.Queries.CreateNode(ctx, generated.CreateNodeParams{
		Name: "bench-node", Address: "10.0.0.1", Port: 62050, ApiPort: 62051, UsageCoefficient: 1,
		ReportSecret: pgtype.Text{String: secret, Valid: true}, CoreOverrides: []byte("{}"),
	}); err != nil {
		b.Fatalf("CreateNode: %v", err)
	}

	poll := func(ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/internal/node-config", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	first := poll("")
	if first.Code != 200 {
		b.Fatalf("first poll: %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	b.Logf("node-config body is %.2f MB, %d users x %d inbounds", float64(first.Body.Len())/(1024*1024), benchNodeConfigUsers, benchNodeConfigInbounds)

	run := func(name string, before func(), ifNoneMatch string, want int) {
		b.Run(name, func(b *testing.B) {
			loadsBefore := handler.nodeConfig.snapshotLoads.Load()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if before != nil {
					b.StopTimer()
					before()
					b.StartTimer()
				}
				if rec := poll(ifNoneMatch); rec.Code != want {
					b.Fatalf("poll: %d, want %d", rec.Code, want)
				}
			}
			b.ReportMetric(float64(handler.nodeConfig.snapshotLoads.Load()-loadsBefore)/float64(b.N), "rebuilds/poll")
		})
	}
	run("changed_full_rebuild", func() {
		if err := handler.store.InvalidateNodeConfigPayload(ctx); err != nil {
			b.Fatalf("invalidate: %v", err)
		}
	}, "", 200)
	run("unchanged_full_body", nil, "", 200)
	run("unchanged_if_none_match_304", nil, etag, 304)
}
