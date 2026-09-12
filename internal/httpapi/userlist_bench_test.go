package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// BenchmarkListUsersPage measures ONE page of GET /api/users against a
// database holding productionUserCount users - the exact shape of the
// production hot path, where a reseller bot walks the whole account list in
// 100-user pages back to back.
//
// It is a benchmark, not a test, so `go test ./...` never runs it; it exists
// to answer "where does the panel's CPU go during a bot sweep" with a
// profile instead of a guess:
//
//	go test ./internal/httpapi -run '^$' -bench ListUsersPage -cpuprofile cpu.out
//	go tool pprof -top -nodecount=25 cpu.out
const (
	benchUserCount  = 10000
	benchPageSize   = 100
	benchPageOffset = 5000
)

var benchNote = "seeded for the /api/users benchmark"

func seedBenchUsers(b *testing.B, h *Handler) {
	b.Helper()
	ctx := context.Background()
	q := h.store.Queries

	// One inbound per protocol the seeded proxies use, so buildUserResponses
	// has a non-empty known-tag set to subtract exclusions from - an empty
	// one would skip most of the per-user work and flatter the result.
	for _, tag := range []string{"VLESS_TCP", "VLESS_WS", "VMESS_TCP"} {
		proto := "vless"
		if tag == "VMESS_TCP" {
			proto = "vmess"
		}
		if _, err := q.UpsertInbound(ctx, generated.UpsertInboundParams{
			Tag: tag, Protocol: proto, Network: "tcp", Security: "none",
		}); err != nil {
			b.Fatalf("UpsertInbound(%s): %v", tag, err)
		}
	}

	for i := 0; i < benchUserCount; i++ {
		u, err := q.CreateUser(ctx, generated.CreateUserParams{
			Username:               fmt.Sprintf("benchuser%05d", i),
			Status:                 statusActive,
			DataLimit:              pgInt8FromInt64(50 << 30),
			Expire:                 pgInt4FromInt(2000000000),
			DataLimitResetStrategy: "no_reset",
			Note:                   textFromPtr(&benchNote),
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

func BenchmarkListUsersPage(b *testing.B) {
	router, token, handler := newTestRouterAndHandler(b)
	seedBenchUsers(b, handler)

	path := fmt.Sprintf("/api/users?offset=%d&limit=%d", benchPageOffset, benchPageSize)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
}
