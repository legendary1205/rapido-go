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
	"github.com/sagernet/sing-box/protocol/anytls"
	"github.com/sagernet/sing-box/protocol/block"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	boxhttp "github.com/sagernet/sing-box/protocol/http"
	"github.com/sagernet/sing-box/protocol/hysteria"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/shadowtls"
	"github.com/sagernet/sing-box/protocol/snell"
	"github.com/sagernet/sing-box/protocol/socks"
	"github.com/sagernet/sing-box/protocol/ssh"
	"github.com/sagernet/sing-box/protocol/tor"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/tuic"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/vmess"

	forkedhysteria2 "github.com/legendary1205/rapido-go/internal/nodecore/hysteria2"
	forkedshadowsocks "github.com/legendary1205/rapido-go/internal/nodecore/shadowsocks"
	forkedtrojan "github.com/legendary1205/rapido-go/internal/nodecore/trojan"
	forkedtuic "github.com/legendary1205/rapido-go/internal/nodecore/tuic"
	forkedvless "github.com/legendary1205/rapido-go/internal/nodecore/vless"
	forkedvmess "github.com/legendary1205/rapido-go/internal/nodecore/vmess"
)

// InboundRegistry registers only the protocols Rapido actually serves, all
// from the local forks - not sing-box's full protocol/transport surface (tun,
// socks, http proxy, WireGuard, etc.), none of which this node needs. The
// upstream vmess/trojan/shadowsocks/hysteria2/tuic packages are still
// imported here (see OutboundRegistry), but only for their outbounds - the
// hysteria2/tuic *inbound* type strings below resolve to this package's own
// forks, not sing-box's.
func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()
	forkedvmess.RegisterInbound(registry)
	forkedtrojan.RegisterInbound(registry)
	forkedshadowsocks.RegisterInbound(registry)
	forkedvless.RegisterInbound(registry)
	forkedhysteria2.RegisterInbound(registry)
	forkedtuic.RegisterInbound(registry)
	return registry
}

// OutboundRegistry registers every outbound type Core Config can produce
// (see cmd/node/main.go's buildCoreOptions/leafOutboundOptions): direct and
// block are always present (a node's two implicit fallback targets even
// with zero custom Core Config outbounds); everything else is registered so
// an admin-defined custom outbound of that type actually has something to
// construct it. This is sing-box's complete outbound surface with three
// deliberate exceptions - see validOutboundTypes's own doc comment in
// internal/httpapi/coreconfig.go for why WireGuard, dns, bridge and Naive
// aren't among them.
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
	anytls.RegisterOutbound(registry)
	hysteria.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	snell.RegisterOutbound(registry)
	ssh.RegisterOutbound(registry)
	tor.RegisterOutbound(registry)
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
