// Package shadowsocks is a fork of sing-box's multi-user shadowsocks inbound
// (github.com/sagernet/sing-box@v1.14.0, protocol/shadowsocks/inbound_multi.go
// plus stubPacketConn from inbound.go) with the same two additions as
// internal/nodecore/vless: an exported UpdateUsers and per-user byte counting.
// Users are keyed by userkey.Key; see that package for why.
//
// Also different: the multi-user service is always built, even with zero users
// (upstream then picks the single-user inbound, which cannot be updated); only
// classic AEAD methods are accepted, as a multi-user 2022 method needs a server
// key this node is never given; relay and managed-server/SSM hooks are dropped.
//
// UpdateUsers swaps in a freshly built service instead of calling
// UpdateUsersWithPasswords on the live one, which rewrites the map that
// connection handlers are ranging over (Go aborts the process on that). The
// cost: a UDP session open during an update re-creates its upstream socket on
// its next packet. TCP connections are untouched.
//
// Re-sync against upstream on every sing-box upgrade.
package shadowsocks

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	"github.com/legendary1205/rapido-go/internal/nodecore/userkey"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocks, NewInbound)
}

var _ adapter.TCPInjectableInbound = (*Inbound)(nil)

type Inbound struct {
	inbound.Adapter
	ctx        context.Context
	router     adapter.ConnectionRouterEx
	logger     logger.ContextLogger
	listener   *listener.Listener
	method     string
	udpTimeout int64
	service    atomic.Pointer[shadowaead.MultiService[*userkey.Key]]
	trafficMgr *traffic.Manager
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
	if len(options.Destinations) > 0 {
		return nil, E.New("shadowsocks relay destinations are not supported")
	} else if options.Managed {
		return nil, E.New("managed shadowsocks servers are not supported")
	}
	switch {
	case common.Contains(shadowaead_2022.List, options.Method):
		return nil, E.New("shadowsocks method ", options.Method, " needs a server key, which a multi-user inbound here is never given: use a classic AEAD method such as chacha20-ietf-poly1305")
	case !common.Contains(shadowaead.List, options.Method):
		return nil, E.New("unsupported method: " + options.Method)
	}
	inbound := &Inbound{
		Adapter:    inbound.NewAdapter(C.TypeShadowsocks, tag),
		ctx:        ctx,
		router:     uot.NewRouter(router, logger),
		logger:     logger,
		method:     options.Method,
		trafficMgr: traffic.FromContext(ctx),
	}
	var err error
	inbound.router, err = mux.NewRouterWithOptions(inbound.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	inbound.udpTimeout = int64(udpTimeout.Seconds())
	err = inbound.UpdateUsers(options.Users)
	if err != nil {
		return nil, err
	}
	inbound.listener = listener.New(listener.Options{
		Context:                  ctx,
		Logger:                   logger,
		Network:                  options.Network.Build(),
		Listen:                   options.ListenOptions,
		ConnectionHandler:        inbound,
		PacketHandler:            inbound,
		ThreadUnsafePacketWriter: true,
	})
	return inbound, nil
}

// UpdateUsers replaces the user list. TCP connections that already passed the
// handshake are unaffected; the method cannot change, only the users.
func (h *Inbound) UpdateUsers(users []option.ShadowsocksUser) error {
	service, err := shadowaead.NewMultiService[*userkey.Key](
		h.method,
		h.udpTimeout,
		adapter.NewLegacyUpstreamHandler(adapter.InboundContext{}, h.newConnection, h.newPacketConnection, h),
	)
	if err != nil {
		return err
	}
	err = service.UpdateUsersWithPasswords(userkey.Build(common.Map(users, func(user option.ShadowsocksUser) string {
		return user.Name
	})), common.Map(users, func(user option.ShadowsocksUser) string {
		return user.Password
	}))
	if err != nil {
		return err
	}
	h.service.Store(service)
	return nil
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

//nolint:staticcheck
func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	err := h.service.Load().NewConnection(ctx, conn, adapter.UpstreamMetadata(metadata))
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil {
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

//nolint:staticcheck
func (h *Inbound) NewPacket(buffer *buf.Buffer, source M.Socksaddr) {
	err := h.service.Load().NewPacket(h.ctx, &stubPacketConn{h.listener.PacketWriter()}, buffer, M.Metadata{Source: source})
	if err != nil {
		h.logger.Error(E.Cause(err, "process packet from ", source))
	}
}

func (h *Inbound) newConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	key, loaded := auth.UserFromContext[*userkey.Key](ctx)
	if !loaded {
		return os.ErrInvalid
	}
	user := key.Label
	if key.Name != "" {
		metadata.User = key.Name
	}
	h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	if h.trafficMgr != nil {
		// This inbound is on the legacy blocking handler interface: RouteConnection
		// returns only once the connection is over (or refused), so the deferred
		// close is this connection's onClose. It runs on every return below.
		defer h.trafficMgr.OpenConn(user, traffic.LocalPort(conn, h.listener.ListenOptions().ListenPort))()
		conn = traffic.WrapConn(conn, user, h.trafficMgr)
	}
	return h.router.RouteConnection(ctx, conn, metadata)
}

func (h *Inbound) newPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	key, loaded := auth.UserFromContext[*userkey.Key](ctx)
	if !loaded {
		return os.ErrInvalid
	}
	user := key.Label
	if key.Name != "" {
		metadata.User = key.Name
	}
	ctx = log.ContextWithNewID(ctx)
	h.logger.InfoContext(ctx, "[", user, "] inbound packet connection from ", metadata.Source)
	h.logger.InfoContext(ctx, "[", user, "] inbound packet connection to ", metadata.Destination)
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	if h.trafficMgr != nil {
		conn = traffic.WrapPacketConn(conn, user, h.trafficMgr)
	}
	return h.router.RoutePacketConnection(ctx, conn, metadata)
}

//nolint:staticcheck
func (h *Inbound) NewError(ctx context.Context, err error) {
	common.Close(err)
	if E.IsClosedOrCanceled(err) {
		h.logger.DebugContext(ctx, "connection closed: ", err)
		return
	}
	h.logger.ErrorContext(ctx, err)
}

var _ N.PacketConn = (*stubPacketConn)(nil)

type stubPacketConn struct {
	N.PacketWriter
}

func (c *stubPacketConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	panic("stub!")
}

func (c *stubPacketConn) Close() error {
	return nil
}

func (c *stubPacketConn) LocalAddr() net.Addr {
	panic("stub!")
}

func (c *stubPacketConn) SetDeadline(t time.Time) error {
	panic("stub!")
}

func (c *stubPacketConn) SetReadDeadline(t time.Time) error {
	panic("stub!")
}

func (c *stubPacketConn) SetWriteDeadline(t time.Time) error {
	panic("stub!")
}
