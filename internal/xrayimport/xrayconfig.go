package xrayimport

import "encoding/json"

// The structs below mirror Xray-core's own real config JSON shape (not
// this codebase's own DTOs) - deliberately permissive (most fields
// pointers or omittable) since a real hand-edited config in the wild
// varies a lot in which optional sections are present at all. `Port` and
// `LocalPort` are json.RawMessage because Xray accepts either a bare
// number or a comma-separated string ("20000,20004,20008,20012") for the
// same field - decided at parse time, not unmarshal time.

type xrayConfig struct {
	Log       xrayLog        `json:"log"`
	DNS       xrayDNS        `json:"dns"`
	Inbounds  []xrayInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
	Routing   xrayRouting    `json:"routing"`
}

type xrayLog struct {
	LogLevel string `json:"loglevel"`
}

type xrayDNS struct {
	// Xray accepts either a bare IP string or a structured object per
	// server entry - RawMessage so parseDNSServers can tell which.
	Servers []json.RawMessage `json:"servers"`
}

type xrayInbound struct {
	Tag            string              `json:"tag"`
	Port           json.RawMessage     `json:"port"`
	Protocol       string              `json:"protocol"`
	Settings       xrayInboundSettings `json:"settings"`
	StreamSettings xrayStreamSettings  `json:"streamSettings"`
	Sniffing       xraySniffing        `json:"sniffing"`
}

type xrayInboundSettings struct {
	Fallbacks []json.RawMessage `json:"fallbacks"`
}

type xraySniffing struct {
	Enabled bool `json:"enabled"`
}

type xrayStreamSettings struct {
	Network         string               `json:"network"`
	Security        string               `json:"security"`
	TLSSettings     *xrayTLSSettings     `json:"tlsSettings"`
	RealitySettings *xrayRealitySettings `json:"realitySettings"`
	TCPSettings     *xrayTCPSettings     `json:"tcpSettings"`
	Sockopt         *xraySockopt         `json:"sockopt"`
}

type xrayTLSSettings struct {
	ServerName   string            `json:"serverName"`
	Fingerprint  string            `json:"fingerprint"`
	Alpn         []string          `json:"alpn"`
	MinVersion   string            `json:"minVersion"`
	Certificates []xrayCertificate `json:"certificates"`
}

type xrayCertificate struct {
	Usage       string   `json:"usage"`
	Certificate []string `json:"certificate"`
	Key         []string `json:"key"`
}

type xrayRealitySettings struct {
	PrivateKey  string   `json:"privateKey"`
	ShortIds    []string `json:"shortIds"`
	ServerNames []string `json:"serverNames"`
	Dest        string   `json:"dest"`
}

type xrayTCPSettings struct {
	Header xrayHeader `json:"header"`
}

type xrayHeader struct {
	Type string `json:"type"`
}

type xraySockopt struct {
	Interface string `json:"interface"`
}

type xrayOutbound struct {
	Tag            string             `json:"tag"`
	Protocol       string             `json:"protocol"`
	Settings       json.RawMessage    `json:"settings"`
	StreamSettings xrayStreamSettings `json:"streamSettings"`
}

// xrayOutboundVnextSettings/xrayOutboundServerSettings are the two shapes
// Xray's outbound `settings` takes depending on protocol - vmess/vless use
// a `vnext` array, shadowsocks/trojan/socks/http use a `servers` array.
// Parsed best-effort: real-world configs vary in exactly which optional
// sub-fields are present, and this importer's primary real-world target
// (per the user's own sample) is freedom/blackhole outbounds, not proxy
// chaining - a proxy outbound that doesn't match either shape is skipped
// with a warning rather than guessed at.
type xrayOutboundVnextSettings struct {
	Vnext []struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
		Users   []struct {
			ID       string `json:"id"`
			Security string `json:"security"`
			Flow     string `json:"flow"`
		} `json:"users"`
	} `json:"vnext"`
}

type xrayOutboundServerSettings struct {
	Servers []struct {
		Address  string `json:"address"`
		Port     int    `json:"port"`
		Password string `json:"password"`
		Method   string `json:"method"`
		User     string `json:"user"`
	} `json:"servers"`
}

type xrayRouting struct {
	DomainStrategy string            `json:"domainStrategy"`
	Rules          []xrayRoutingRule `json:"rules"`
}

type xrayRoutingRule struct {
	Type        string          `json:"type"`
	InboundTag  []string        `json:"inboundTag"`
	LocalPort   json.RawMessage `json:"localPort"`
	OutboundTag string          `json:"outboundTag"`
}
