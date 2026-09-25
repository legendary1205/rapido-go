// Package vless is a minimal fork of sing-box's protocol/vless inbound
// (github.com/sagernet/sing-box@v1.14.0, protocol/vless/inbound.go),
// copied verbatim except for three additions: UpdateUsers, an exported method
// that calls the same underlying sing-vmess vless.Service.UpdateUsers the
// original constructor already calls internally; per-user byte counting on
// every accepted connection; and userkey.Key user identities in place of
// option-slice indexes.
//
// Why this fork exists: sing-box's own Inbound keeps that service on an
// unexported field, so a caller outside this package - i.e. the rest of
// this repo - has no way to add/remove a user on a running inbound without
// tearing the whole listener down and rebuilding it, unlike Xray's
// HandlerService.AlterInbound gRPC call, which Rapido's current Python
// panel uses to hot-add/remove users with zero connection loss. Everything
// else here (TLS/REALITY, transport, mux, listener lifecycle) is untouched
// sing-box logic, imported normally - only the struct definition needs to
// live in this package so UpdateUsers can reach the private `service` and
// `users` fields.
//
// Re-sync this file against sing-box's own protocol/vless/inbound.go on
// every sing-box upgrade; a diff is the fastest way to catch upstream
// changes this fork needs to absorb.
package vless

import (
	"context"
	"net"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing-vmess/packetaddr"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	"github.com/legendary1205/rapido-go/internal/nodecore/userkey"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.VLESSInboundOptions](registry, C.TypeVLESS, NewInbound)
}

var _ adapter.TCPInjectableInbound = (*Inbound)(nil)

type Inbound struct {
	inbound.Adapter
	ctx        context.Context
	router     adapter.ConnectionRouterEx
	logger     logger.ContextLogger
	listener   *listener.Listener
	service    *vless.Service[*userkey.Key]
	tlsConfig  tls.ServerConfig
	transport  adapter.V2RayServerTransport
	references []string
	trafficMgr *traffic.Manager
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.VLESSInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter:    inbound.NewAdapter(C.TypeVLESS, tag),
		ctx:        ctx,
		router:     uot.NewRouter(router, logger),
		logger:     logger,
		trafficMgr: traffic.FromContext(ctx),
	}
	var err error
	inbound.router, err = mux.NewRouterWithOptions(inbound.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	inbound.service = vless.NewService[*userkey.Key](logger, adapter.NewUpstreamContextHandler(inbound.newConnectionEx, inbound.newPacketConnectionEx))
	inbound.UpdateUsers(options.Users)
	if options.TLS != nil {
		inbound.tlsConfig, err = tls.NewServerWithOptions(tls.ServerOptions{
			Context: ctx,
			Logger:  logger,
			Options: common.PtrValueOrDefault(options.TLS),
			KTLSCompatible: common.PtrValueOrDefault(options.Transport).Type == "" &&
				!common.PtrValueOrDefault(options.Multiplex).Enabled &&
				common.All(options.Users, func(it option.VLESSUser) bool {
					return it.Flow == ""
				}),
		})
		if err != nil {
			return nil, err
		}
		if options.TLS.Reality != nil && options.TLS.Reality.Enabled && options.TLS.Reality.Handshake.Detour != "" {
			inbound.references = []string{options.TLS.Reality.Handshake.Detour}
		}
	}
	if options.Transport != nil {
		inbound.transport, err = v2ray.NewServerTransport(ctx, logger, common.PtrValueOrDefault(options.Transport), inbound.tlsConfig, (*inboundTransportHandler)(inbound))
		if err != nil {
			return nil, E.Cause(err, "create server transport: ", options.Transport.Type)
		}
	}
	inbound.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inbound,
	})
	return inbound, nil
}

// UpdateUsers replaces the inbound's user list in place, on the already-
// running listener/TLS/transport - the addition this fork exists for. This
// is exactly what NewInbound does internally to seed the initial user list;
// calling it again is what sing-vmess's vless.Service is designed to
// support, sing-box's own adapter.Inbound just never exposes the hook.
//
// Users are keyed by userkey.Key rather than upstream's index into the option
// slice; see that package for why.
func (h *Inbound) UpdateUsers(users []option.VLESSUser) {
	h.service.UpdateUsers(
		userkey.Build(common.Map(users, func(it option.VLESSUser) string { return it.Name })),
		common.Map(users, func(it option.VLESSUser) string { return it.UUID }),
		common.Map(users, func(it option.VLESSUser) string { return it.Flow }),
	)
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
	if h.transport == nil {
		return h.listener.Start()
	}
	if common.Contains(h.transport.Network(), N.NetworkTCP) {
		tcpListener, err := h.listener.ListenTCP()
		if err != nil {
			return err
		}
		go func() {
			sErr := h.transport.Serve(tcpListener)
			if sErr != nil && !E.IsClosed(sErr) {
				h.logger.Error("transport serve error: ", sErr)
			}
		}()
	}
	if common.Contains(h.transport.Network(), N.NetworkUDP) {
		udpConn, err := h.listener.ListenUDP()
		if err != nil {
			return err
		}
		go func() {
			sErr := h.transport.ServePacket(udpConn)
			if sErr != nil && !E.IsClosed(sErr) {
				h.logger.Error("transport serve error: ", sErr)
			}
		}()
	}
	return nil
}

func (h *Inbound) Close() error {
	return common.Close(
		h.service,
		h.listener,
		h.tlsConfig,
		h.transport,
	)
}

func (h *Inbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if h.tlsConfig != nil && h.transport == nil {
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

func (h *Inbound) newConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
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
		conn = traffic.WrapConn(conn, user, h.trafficMgr)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (h *Inbound) newPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
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
	if metadata.Destination.Fqdn == packetaddr.SeqPacketMagicAddress {
		metadata.Destination = M.Socksaddr{}
		conn = packetaddr.NewConn(bufio.NewNetPacketConn(conn), metadata.Destination)
		h.logger.InfoContext(ctx, "[", user, "] inbound packet addr connection")
	} else {
		h.logger.InfoContext(ctx, "[", user, "] inbound packet connection to ", metadata.Destination)
	}
	if h.trafficMgr != nil {
		conn = traffic.WrapPacketConn(conn, user, h.trafficMgr)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

var _ adapter.V2RayServerTransportHandler = (*inboundTransportHandler)(nil)

type inboundTransportHandler Inbound

func (h *inboundTransportHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Source = source
	metadata.Destination = destination
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	h.logger.InfoContext(ctx, "inbound connection from ", metadata.Source)
	(*Inbound)(h).NewConnection(ctx, conn, metadata, onClose)
}

func (h *Inbound) References() []string {
	return h.references
}
