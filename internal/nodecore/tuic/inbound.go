// Package tuic is a minimal fork of sing-box's protocol/tuic inbound
// (github.com/sagernet/sing-box@v1.14.0, protocol/tuic/inbound.go), copied
// verbatim except for the same three additions the other forks in this
// package make: UpdateUsers, an exported method that calls the same
// underlying sing-quic/tuic.Service[U].UpdateUsers the original constructor
// already calls internally; per-user byte counting and connection tracking
// on every accepted connection; and userkey.Key user identities in place of
// sing-box's own plain int index - see vless's own doc comment for the
// hot-update race an index has that a Key does not. TUIC's own auth is by
// UUID (sing-quic's Service[U] keys its user map by the UUID, U is only the
// opaque identity attached once authenticated), unlike hysteria2's
// password-keyed map - see internal/nodecore/hysteria2's own doc comment
// for that difference.
//
// Re-sync this file against sing-box's own protocol/tuic/inbound.go on every
// sing-box upgrade; a diff is the fastest way to catch upstream changes this
// fork needs to absorb.
package tuic

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	"github.com/legendary1205/rapido-go/internal/nodecore/userkey"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.TUICInboundOptions](registry, C.TypeTUIC, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	router     adapter.ConnectionRouterEx
	logger     log.ContextLogger
	listener   *listener.Listener
	tlsConfig  tls.ServerConfig
	server     *tuic.Service[*userkey.Key]
	trafficMgr *traffic.Manager
	conns      *traffic.ConnGroup
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TUICInboundOptions) (adapter.Inbound, error) {
	options.UDPFragmentDefault = true
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}
	inb := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeTUIC, tag),
		router:  uot.NewRouter(router, logger),
		logger:  logger,
		listener: listener.New(listener.Options{
			Context: ctx,
			Logger:  logger,
			Listen:  options.ListenOptions,
		}),
		tlsConfig:  tlsConfig,
		trafficMgr: traffic.FromContext(ctx),
		conns:      traffic.ConnGroupFromContext(ctx),
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	service, err := tuic.NewService[*userkey.Key](tuic.ServiceOptions{
		Context:   ctx,
		Logger:    logger,
		TLSConfig: tlsConfig,
		QUICOptions: qtls.QUICOptions{
			IdleTimeout:             options.IdleTimeout.Build(),
			KeepAlivePeriod:         options.KeepAlivePeriod.Build(),
			StreamReceiveWindow:     options.StreamReceiveWindow.Value(),
			ConnectionReceiveWindow: options.ConnectionReceiveWindow.Value(),
			MaxConcurrentStreams:    options.MaxConcurrentStreams,
			InitialPacketSize:       options.InitialPacketSize,
			DisablePathMTUDiscovery: options.DisablePathMTUDiscovery,
		},
		CongestionControl: options.CongestionControl,
		AuthTimeout:       time.Duration(options.AuthTimeout),
		ZeroRTTHandshake:  options.ZeroRTTHandshake,
		Heartbeat:         time.Duration(options.Heartbeat),
		UDPTimeout:        udpTimeout,
		Handler:           inb,
	})
	if err != nil {
		return nil, err
	}
	inb.server = service
	if err := inb.UpdateUsers(options.Users); err != nil {
		return nil, err
	}
	return inb, nil
}

// UpdateUsers replaces the inbound's user list in place, on the already-
// running QUIC listener - the addition this fork exists for. Every user
// needs a real UUID (TUIC's own auth key); a missing/invalid one is an
// error, changing nothing, exactly like the original constructor's own
// per-user validation.
func (h *Inbound) UpdateUsers(users []option.TUICUser) error {
	keys := userkey.Build(common.Map(users, func(it option.TUICUser) string { return it.Name }))
	uuidList := make([][16]byte, len(users))
	passwordList := make([]string, len(users))
	for index, user := range users {
		if user.UUID == "" {
			return E.New("missing uuid for user ", index)
		}
		userUUID, err := uuid.FromString(user.UUID)
		if err != nil {
			return E.Cause(err, "invalid uuid for user ", index)
		}
		uuidList[index] = userUUID
		passwordList[index] = user.Password
	}
	h.server.UpdateUsers(keys, uuidList, passwordList)
	return nil
}

func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx = log.ContextWithNewID(ctx)
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.OriginDestination = h.listener.UDPAddr()
	metadata.Source = source
	metadata.Destination = destination
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

func (h *Inbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	ctx = log.ContextWithNewID(ctx)
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.OriginDestination = h.listener.UDPAddr()
	metadata.Source = source
	metadata.Destination = destination
	key, loaded := auth.UserFromContext[*userkey.Key](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	user := key.Label
	if key.Name != "" {
		metadata.User = key.Name
	}
	h.logger.InfoContext(ctx, "[", user, "] inbound packet connection to ", metadata.Destination)
	onClose = h.conns.Track(conn, onClose)
	if h.trafficMgr != nil {
		conn = traffic.WrapPacketConn(conn, user, h.trafficMgr)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		err := h.tlsConfig.Start()
		if err != nil {
			return err
		}
	}
	packetConn, err := h.listener.ListenUDP()
	if err != nil {
		return err
	}
	return h.server.Start(packetConn)
}

func (h *Inbound) Close() error {
	return common.Close(
		h.listener,
		h.tlsConfig,
		common.PtrOrNil(h.server),
	)
}
