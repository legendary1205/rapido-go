// Package nodecore wraps a sing-box instance for Rapido's node agent, using
// a locally-forked VLESS inbound (internal/nodecore/vless) in place of
// sing-box's own so users can be hot-added/removed on a running listener -
// see that package's doc comment for why. Every other protocol/outbound
// registered here is unmodified upstream sing-box code.
package nodecore

import (
	boxCertificate "github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxService "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/vmess"

	forkedvless "github.com/legendary1205/rapido-go/internal/nodecore/vless"
)

// InboundRegistry registers only the protocols Rapido actually serves
// (vmess/trojan/shadowsocks from upstream sing-box, vless from the local
// fork) - not sing-box's full protocol/transport surface (tun, socks,
// http proxy, WireGuard, etc.), none of which this node needs.
func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()
	vmess.RegisterInbound(registry)
	trojan.RegisterInbound(registry)
	shadowsocks.RegisterInbound(registry)
	forkedvless.RegisterInbound(registry)
	return registry
}

// OutboundRegistry registers "direct" only - proxied connections are
// forwarded straight to their destination, matching how Rapido's nodes
// operate today (no outbound chaining/selection).
func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()
	direct.RegisterOutbound(registry)
	return registry
}

// The remaining registries are required by box.New but unused by Rapido's
// node (no WireGuard/Tailscale endpoints, no custom DNS transports beyond
// the box default, no extra services, no ACME) - empty is a valid registry.
func EndpointRegistry() *endpoint.Registry { return endpoint.NewRegistry() }

// The "local" transport is the one box.New falls back to by default when a
// config declares no DNS servers of its own - required even though Rapido's
// simple direct-proxy config never actually resolves a domain today.
func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()
	local.RegisterTransport(registry)
	return registry
}
func ServiceRegistry() *boxService.Registry                 { return boxService.NewRegistry() }
func CertificateProviderRegistry() *boxCertificate.Registry { return boxCertificate.NewRegistry() }
