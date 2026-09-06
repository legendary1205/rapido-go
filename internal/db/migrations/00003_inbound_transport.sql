-- +goose Up
-- Same gap as 00002_inbound_protocol.sql: the current Python system reads
-- an inbound's transport shape (network: tcp/ws/grpc/kcp/quic/splithttp/
-- xhttp, and header_type: e.g. "http" for tcp obfuscation) from the live
-- parsed xray/sing-box config, not the DB - needed by subscription
-- generation (Phase 4) to build the right link/config shape per inbound.
-- Extends the same POST /api/inbounds/sync stand-in from Phase 3.
ALTER TABLE inbounds ADD COLUMN network TEXT NOT NULL DEFAULT 'tcp';
ALTER TABLE inbounds ADD COLUMN header_type TEXT;

-- +goose Down
ALTER TABLE inbounds DROP COLUMN header_type;
ALTER TABLE inbounds DROP COLUMN network;
