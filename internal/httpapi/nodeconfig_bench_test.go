package httpapi

import (
	"context"
	"fmt"
	"testing"

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

	body, err := handler.buildNodeConfigBody(ctx)
	if err != nil {
		b.Fatalf("buildNodeConfigBody: %v", err)
	}
	b.Logf("node-config body is %.2f MB", float64(len(body))/(1024*1024))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := handler.buildNodeConfigBody(ctx); err != nil {
			b.Fatalf("buildNodeConfigBody: %v", err)
		}
	}
}
