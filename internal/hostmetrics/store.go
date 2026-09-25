package hostmetrics

import (
	"context"
	"encoding/json"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// payloadCap mirrors collect_metrics.py's own json.dumps(sample)[:65000] -
// the raw sample dumped into host_metrics.payload for any field not worth
// its own column, capped so one oversized sample (an interface list that
// somehow grew huge) can't blow up a TEXT column.
const payloadCap = 65000

// Store persists one sample (already reduced to rates/percents via a
// PreviousTracker) as a host_metrics row. nodeID nil means the panel's own
// self-sample, the same convention every usage table in this schema uses
// for "not a specific node".
func Store(ctx context.Context, queries *generated.Queries, nodeID *int32, s Sample, d Derived, healthy bool) error {
	payload, err := json.Marshal(s)
	if err != nil {
		payload = nil
	}
	if len(payload) > payloadCap {
		payload = payload[:payloadCap]
	}

	tunnelsUp := 0
	for _, t := range s.Tunnels {
		if t.Up {
			tunnelsUp++
		}
	}

	_, err = queries.InsertHostMetric(ctx, generated.InsertHostMetricParams{
		NodeID:       pgInt4FromPtr(nodeID),
		CollectedAt:  pgtype.Timestamptz{Time: s.CollectedAt, Valid: true},
		CpuPercent:   pgFloat8FromPtr(d.CPUPercent),
		MemPercent:   pgFloat8FromPtr(d.MemPercent),
		DiskPercent:  pgFloat8FromPtr(d.DiskPercent),
		RxRate:       pgInt8FromPtr(d.RxRate),
		TxRate:       pgInt8FromPtr(d.TxRate),
		Connections:  pgtype.Int4{Int32: int32(s.ConnectionsEstablished), Valid: true},
		TunnelsUp:    pgtype.Int4{Int32: int32(tunnelsUp), Valid: true},
		TunnelsTotal: pgtype.Int4{Int32: int32(len(s.Tunnels)), Valid: true},
		Healthy:      healthy,
		Payload:      pgtype.Text{String: string(payload), Valid: len(payload) > 0},
		ClientConns:  clientConnsColumn(s),
	})
	return err
}

// clientConnsColumn is host_metrics.client_conns: the node's open client
// connections, or NULL when the sample does not carry a reading (a node that
// predates the field, the panel's own sample).
func clientConnsColumn(s Sample) pgtype.Int4 {
	if !s.ClientConnectionsKnown() {
		return pgtype.Int4{}
	}
	n := s.ClientConnections
	if n < 0 {
		n = 0
	}
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	return pgtype.Int4{Int32: int32(n), Valid: true}
}

func pgInt4FromPtr(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}

func pgFloat8FromPtr(v *float64) pgtype.Float8 {
	if v == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *v, Valid: true}
}

func pgInt8FromPtr(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
