// Package snell is a minimal fork of sing-box's protocol/snell inbound
// (github.com/sagernet/sing-box@v1.14.0, protocol/snell/inbound.go), copied
// verbatim except for the same three additions the other forks in this
// package make: UpdateUsers, an exported method that calls the same
// underlying sing-snell MultiService[U].UpdateUsers the constructor already
// calls internally; per-user byte counting and connection tracking on every
// accepted connection; and userkey.Key user identities in place of
// sing-box's own plain int index - see the vless package's own doc comment
// for the hot-update race an index has that a Key does not. sing-snell's
// MultiService[U] is generic over a comparable U, keyed by the client's own
// user key (a byte string, not an index), so *userkey.Key drops in exactly
// like it does for the QUIC-based forks.
//
// Two scope cuts from upstream, both deliberate:
//
//   - Version is fixed at 6, not exposed as an admin choice. sing-box's
//     snell package is really two separate, non-interoperating ecosystems:
//     v5 exists ONLY as a server here (github.com/sagernet/sing-snell has no
//     v5 client package at all) - it exists to be wire-compatible with the
//     real official snell-server v5 binary Surge and other Snell-native
//     tools speak, not with anything sing-box itself can dial. v4 is the
//     reverse: a client only, no v5-family server. v6 is the one version
//     with both a real client and a real server in this dependency tree, so
//     it is the version any sing-box-based client (NekoBox, husi, ...) can
//     actually connect through - NOT including this codebase's own Core
//     Config snell outbound, which is hardcoded to v4 (see
//     cmd/node/main.go's leafOutboundOptions), so it cannot dial this
//     inbound either; the two were built in separate, unrelated requests
//     and nothing here changes that. A Surge client cannot use this inbound
//     at all; that limitation is inherent to the dependency, not a gap in
//     this fork.
//
//   - This fork always builds the multi-user service, never the anonymous
//     single-PSK one upstream's own constructor falls back to when
//     options.Users is empty - a Rapido inbound with zero configured users
//     should refuse everyone, not fall back to a single shared PSK nothing
//     in this codebase's model has a slot for. See UpdateUsers's own doc
//     comment for how the zero-users case is handled instead.
//
// Re-sync this file against sing-box's own protocol/snell/inbound.go on
// every sing-box upgrade; a diff is the fastest way to catch upstream
// changes this fork needs to absorb.
package snell

import (
	"context"
	"crypto/rand"
	"net"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-snell/snellv6"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	"github.com/legendary1205/rapido-go/internal/nodecore/userkey"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.SnellInboundOptions](registry, C.TypeSnell, NewInbound)
}

var _ adapter.TCPInjectableInbound = (*Inbound)(nil)

type Inbound struct {
	inbound.Adapter
	router     adapter.ConnectionRouterEx
	logger     logger.ContextLogger
	listener   *listener.Listener
	service    *snellv6.MultiService[*userkey.Key]
	trafficMgr *traffic.Manager
	conns      *traffic.ConnGroup
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SnellInboundOptions) (adapter.Inbound, error) {
	if options.Version != 6 {
		return nil, E.New("snell: this build only serves version 6 (got ", options.Version, ") - see this package's own doc comment")
	}
	mode, err := snellv6.ParseMode(options.V6Options.Mode)
	if err != nil {
		return nil, err
	}
	inb := &Inbound{
		Adapter:    inbound.NewAdapter(C.TypeSnell, tag),
		router:     uot.NewRouter(router, logger),
		logger:     logger,
		trafficMgr: traffic.FromContext(ctx),
		conns:      traffic.ConnGroupFromContext(ctx),
	}
	service, err := snellv6.NewMultiService[*userkey.Key](snellv6.ServerOptions{
		PSK: []byte(options.PSK), Mode: mode, Handler: inb,
	})
	if err != nil {
		return nil, err
	}
	inb.service = service
	if err := inb.UpdateUsers(options.Users); err != nil {
		return nil, err
	}
	inb.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inb,
	})
	return inb, nil
}

// UpdateUsers replaces the inbound's user list in place, on the already-
// running listener - the addition this fork exists for. Every user needs a
// real, non-empty UserKey; sing-snell rejects a duplicate one across users
// the same way it rejects a missing one, changing nothing.
//
// A genuinely empty list is not passed through to sing-snell's own
// UpdateUsers, which refuses it outright (snell.ErrNoUsers) rather than
// accepting it as "nobody is authorized" - this substitutes one synthetic
// entry keyed by a fresh random 32-byte secret nothing on the wire could
// ever supply, so the running service still refuses every real client
// exactly as "no users configured" should, without erroring.
func (h *Inbound) UpdateUsers(users []option.SnellUser) error {
	names := make([]string, len(users))
	keys := make([][]byte, len(users))
	for i, u := range users {
		if u.UserKey == "" {
			return E.New("snell: missing userkey for user ", i)
		}
		names[i] = u.Name
		keys[i] = []byte(u.UserKey)
	}
	if len(users) == 0 {
		unreachable := make([]byte, 32)
		if _, err := rand.Read(unreachable); err != nil {
			return err
		}
		names, keys = []string{""}, [][]byte{unreachable}
	}
	return h.service.UpdateUsers(userkey.Build(names), keys)
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	return h.listener.Close()
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	err := h.service.NewConnection(adapter.WithContext(ctx, &metadata), conn, metadata.Source, onClose)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	_, metadata := adapter.ExtendContext(ctx)
	if source.IsValid() {
		metadata.Source = source
	}
	if destination.IsValid() {
		metadata.Destination = destination
	}
	h.newConnection(ctx, conn, *metadata, onClose)
}

func (h *Inbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	_, metadata := adapter.ExtendContext(ctx)
	if source.IsValid() {
		metadata.Source = source
	}
	if destination.IsValid() {
		metadata.Destination = destination
	}
	h.newPacketConnection(ctx, conn, *metadata, onClose)
}

func (h *Inbound) newConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	key, loaded := auth.UserFromContext[*userkey.Key](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	user := key.Label
	if key.Name != "" {
		metadata.User = key.Name
	}
	h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	if h.trafficMgr != nil {
		onClose = h.trafficMgr.TrackClose(user, conn, h.listener.ListenOptions().ListenPort, onClose)
		conn = traffic.WrapConn(conn, user, h.trafficMgr)
	}
	onClose = h.conns.Track(conn, onClose)
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) newPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	// The snell client in Surge rejects UDP responses with domain addresses
	// - matches upstream's own comment on this exact line.
	metadata.UDPDisableDomainUnmapping = true
	key, loaded := auth.UserFromContext[*userkey.Key](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	user := key.Label
	if key.Name != "" {
		metadata.User = key.Name
	}
	h.logger.InfoContext(ctx, "[", user, "] inbound packet connection from ", metadata.Source)
	onClose = h.conns.Track(conn, onClose)
	if h.trafficMgr != nil {
		conn = traffic.WrapPacketConn(conn, user, h.trafficMgr)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}
