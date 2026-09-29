// Package anytls is a minimal fork of sing-box's protocol/anytls inbound
// (github.com/sagernet/sing-box@v1.14.0, protocol/anytls/inbound.go),
// copied verbatim except for the same three additions the other forks in
// this package make: UpdateUsers, an exported method that calls the same
// underlying sing-anytls Service.UpdateUsers the constructor already calls
// internally; per-user byte counting and connection tracking on every
// accepted connection; and reusing sing-anytls's own user identity as-is,
// rather than substituting userkey.Key the way every other fork here does.
//
// That last point needs its own explanation, since every sibling fork's own
// doc comment (see vless's) warns about exactly this: an index that outlives
// the generation it was resolved against is a real hot-update race. AnyTLS's
// own auth.ContextWithUser call happens synchronously inside NewConnection,
// immediately after looking a plain string username up by
// sha256(password) in the CURRENT service.users map - there is no
// index into a list to go stale, and no window between "identity resolved"
// and "identity used" for a concurrent UpdateUsers to open up. The string
// itself is already exactly the stable per-user label traffic counting
// wants (traffic.Manager keys by string username natively), so unlike
// hysteria2/tuic/snell there is no bridging type to introduce here at all.
//
// One real behavior difference from sing-box's own inbound: this fork
// requires TLS (like this codebase's hysteria2/tuic forks) rather than
// treating it as admin-optional - AnyTLS's whole design is to look like an
// ordinary TLS connection, so a plaintext AnyTLS inbound is not a
// configuration this codebase's Core Config model exposes at all (see
// cmd/node/main.go's buildOutboundTLS forcing the same on this protocol's
// outbound side).
//
// Re-sync this file against sing-box's own protocol/anytls/inbound.go on
// every sing-box upgrade; a diff is the fastest way to catch upstream
// changes this fork needs to absorb.
package anytls

import (
	"context"
	"net"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.AnyTLSInboundOptions](registry, C.TypeAnyTLS, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	tlsConfig  tls.ServerConfig
	router     adapter.ConnectionRouterEx
	logger     logger.ContextLogger
	listener   *listener.Listener
	service    *anytls.Service
	trafficMgr *traffic.Manager
	conns      *traffic.ConnGroup
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}
	inb := &Inbound{
		Adapter:    inbound.NewAdapter(C.TypeAnyTLS, tag),
		router:     uot.NewRouter(router, logger),
		logger:     logger,
		tlsConfig:  tlsConfig,
		trafficMgr: traffic.FromContext(ctx),
		conns:      traffic.ConnGroupFromContext(ctx),
	}

	paddingScheme := padding.DefaultPaddingScheme
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}

	service, err := anytls.NewService(anytls.ServiceConfig{
		Users:         common.Map(options.Users, func(it option.AnyTLSUser) anytls.User { return anytls.User(it) }),
		PaddingScheme: paddingScheme,
		Handler:       (*inboundHandler)(inb),
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}
	inb.service = service
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
// running listener - the addition this fork exists for. See this package's
// own doc comment for why no identity-staleness guard is needed here the
// way the other forks' UpdateUsers document.
func (h *Inbound) UpdateUsers(users []option.AnyTLSUser) {
	h.service.UpdateUsers(common.Map(users, func(it option.AnyTLSUser) anytls.User { return anytls.User(it) }))
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
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	return common.Close(h.listener, h.tlsConfig)
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": TLS handshake"))
			return
		}
		conn = tlsConn
	}
	err := h.service.NewConnection(adapter.WithContext(ctx, &metadata), conn, metadata.Source, onClose)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
	}
}

type inboundHandler Inbound

func (h *inboundHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	metadata.Source = source
	metadata.Destination = destination.Unwrap()
	user, _ := auth.UserFromContext[string](ctx)
	if user != "" {
		metadata.User = user
		h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	if h.trafficMgr != nil && user != "" {
		onClose = h.trafficMgr.TrackClose(user, conn, h.listener.ListenOptions().ListenPort, onClose)
		conn = traffic.WrapConn(conn, user, h.trafficMgr)
	}
	onClose = h.conns.Track(conn, onClose)
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
