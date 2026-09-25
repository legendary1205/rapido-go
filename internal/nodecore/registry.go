// Package nodecore wraps a sing-box instance for Rapido's node agent, using
// locally-forked VLESS, VMess, Trojan and Shadowsocks inbounds
// (internal/nodecore/{vless,vmess,trojan,shadowsocks}) in place of sing-box's
// own so users can be hot-added/removed on a running listener and every
// connection's bytes are counted per user - see the vless package's doc
// comment for why. Every other protocol/outbound registered here is
// unmodified upstream sing-box code.
package nodecore

import (
	boxCertificate "github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxService "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	dnstransport "github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/protocol/block"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	boxhttp "github.com/sagernet/sing-box/protocol/http"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/socks"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/tuic"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/vmess"

	forkedshadowsocks "github.com/legendary1205/rapido-go/internal/nodecore/shadowsocks"
	forkedtrojan "github.com/legendary1205/rapido-go/internal/nodecore/trojan"
	forkedvless "github.com/legendary1205/rapido-go/internal/nodecore/vless"
	forkedvmess "github.com/legendary1205/rapido-go/internal/nodecore/vmess"
)

// InboundRegistry registers only the protocols Rapido actually serves, all
// from the local forks - not sing-box's full protocol/transport surface (tun,
// socks, http proxy, WireGuard, etc.), none of which this node needs. The
// upstream vmess/trojan/shadowsocks packages are still imported here, but only
// for their outbounds.
func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()
	forkedvmess.RegisterInbound(registry)
	forkedtrojan.RegisterInbound(registry)
	forkedshadowsocks.RegisterInbound(registry)
	forkedvless.RegisterInbound(registry)
	return registry
}

// OutboundRegistry registers exactly the outbound types Phase 7.4's Core
// Config can produce (see cmd/node/main.go's buildCoreOptions): direct and
// block are always present (a node's two implicit fallback targets even
// with zero custom Core Config outbounds); socks/http/shadowsocks/vmess/
// trojan/vless/hysteria2/tuic/selector/urltest are registered so an
// admin-defined custom outbound of one of those types actually has
// something to construct it. Not sing-box's full outbound surface -
// WireGuard is deliberately excluded (a sing-box "Endpoint", a
// structurally different config section this rewrite doesn't wire in at
// all yet, not just another outbound type - see coreconfig.go's own doc
// comment), and no other protocol (Hysteria v1, TUIC's exotic siblings,
// ShadowsocksR, Naive, Tor, SSH, ShadowTLS, AnyTLS) is something Core
// Config's structured form exposes.
func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()
	direct.RegisterOutbound(registry)
	block.RegisterOutbound(registry)
	socks.RegisterOutbound(registry)
	boxhttp.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)
	hysteria2.RegisterOutbound(registry)
	tuic.RegisterOutbound(registry)
	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)
	return registry
}

// The remaining registries are required by box.New but unused by Rapido's
// node (no WireGuard/Tailscale endpoints, no extra services, no ACME) -
// empty is a valid registry.
func EndpointRegistry() *endpoint.Registry { return endpoint.NewRegistry() }

// DNSTransportRegistry registers "local" (box.New's own fallback when a
// config declares no DNS servers, needed even when Core Config has none
// configured) plus every DNS server type Core Config's structured form can
// produce (see cmd/node/main.go's buildCoreOptions: local/udp/tcp/tls/
// https) - not sing-box's full DNS transport surface (no DHCP/mDNS/
// FakeIP/QUIC/DoH3, none of which Core Config exposes).
func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()
	local.RegisterTransport(registry)
	dnstransport.RegisterUDP(registry)
	dnstransport.RegisterTCP(registry)
	dnstransport.RegisterTLS(registry)
	dnstransport.RegisterHTTPS(registry)
	return registry
}
func ServiceRegistry() *boxService.Registry                 { return boxService.NewRegistry() }
func CertificateProviderRegistry() *boxCertificate.Registry { return boxCertificate.NewRegistry() }
