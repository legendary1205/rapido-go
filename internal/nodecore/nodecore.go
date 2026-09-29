package nodecore

import (
	"context"
	"fmt"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/option"

	forkedhysteria2 "github.com/legendary1205/rapido-go/internal/nodecore/hysteria2"
	forkedshadowsocks "github.com/legendary1205/rapido-go/internal/nodecore/shadowsocks"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	forkedtrojan "github.com/legendary1205/rapido-go/internal/nodecore/trojan"
	forkedtuic "github.com/legendary1205/rapido-go/internal/nodecore/tuic"
	forkedvless "github.com/legendary1205/rapido-go/internal/nodecore/vless"
	forkedvmess "github.com/legendary1205/rapido-go/internal/nodecore/vmess"
)

// Node wraps a running sing-box instance built from the restricted
// registries in registry.go.
type Node struct {
	box     *box.Box
	Traffic *traffic.Manager
	// conns is every client connection this instance has accepted and not yet
	// finished with; Close ends them all. See traffic.ConnGroup.
	conns *traffic.ConnGroup
}

// New builds a sing-box instance from the given options - it does not
// initialize or start anything yet, call Start for that. (Box.Start does
// its own internal pre-start pass; calling Box.PreStart here too would run
// it twice and panic on a double-started component.)
//
// mgr is threaded through sing-box's own inbound-construction context (see
// traffic.NewContext) so the forked inbounds can wrap every accepted
// connection with a byte counter - the panel has no way to poll Xray-style
// stats out of sing-box, so counting happens here, at construction time,
// rather than via any later registration step.
func New(ctx context.Context, opts option.Options, mgr *traffic.Manager) (*Node, error) {
	conns := traffic.NewConnGroup()
	ctx = traffic.WithConnGroup(traffic.NewContext(ctx, mgr), conns)
	ctx = box.Context(ctx, InboundRegistry(), OutboundRegistry(), EndpointRegistry(), DNSTransportRegistry(), ServiceRegistry(), CertificateProviderRegistry())
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: opts,
	})
	if err != nil {
		return nil, fmt.Errorf("nodecore: build instance: %w", err)
	}
	return &Node{box: instance, Traffic: mgr, conns: conns}, nil
}

// Start initializes every component and opens every configured inbound's
// listener.
func (n *Node) Start() error {
	return n.box.Start()
}

// Close tears down every inbound, outbound and background service, then closes
// every client connection the instance had accepted. Closing the box stops the
// listeners and the router, which ends the connections the router is relaying;
// what is left - chiefly a multiplexed session, whose carrier connection the
// inbound reads itself - would keep answering its client with errors from a dead
// router until the client gave up on its own.
func (n *Node) Close() error {
	err := n.box.Close()
	n.conns.CloseAll()
	return err
}

// User is one proxy account in the protocol-neutral shape UpdateUsers takes.
type User struct {
	Name     string
	UUID     string // vless, vmess
	Password string // trojan, shadowsocks
	Flow     string // vless
}

// UpdateUsers replaces the user list on an already-running inbound identified
// by tag, with no listener restart and no impact on connections already
// established. protocol is one of vless, vmess, trojan, shadowsocks,
// hysteria2 or tuic and must match the inbound's actual type. On error the
// running list is unchanged.
func (n *Node) UpdateUsers(tag, protocol string, users []User) error {
	switch protocol {
	case "vless":
		return n.UpdateVLESSUsers(tag, mapUsers(users, func(u User) option.VLESSUser {
			return option.VLESSUser{Name: u.Name, UUID: u.UUID, Flow: u.Flow}
		}))
	case "vmess":
		return n.UpdateVMessUsers(tag, mapUsers(users, func(u User) option.VMessUser {
			return option.VMessUser{Name: u.Name, UUID: u.UUID}
		}))
	case "trojan":
		return n.UpdateTrojanUsers(tag, mapUsers(users, func(u User) option.TrojanUser {
			return option.TrojanUser{Name: u.Name, Password: u.Password}
		}))
	case "shadowsocks":
		return n.UpdateShadowsocksUsers(tag, mapUsers(users, func(u User) option.ShadowsocksUser {
			return option.ShadowsocksUser{Name: u.Name, Password: u.Password}
		}))
	case "hysteria2":
		return n.UpdateHysteria2Users(tag, mapUsers(users, func(u User) option.Hysteria2User {
			return option.Hysteria2User{Name: u.Name, Password: u.Password}
		}))
	case "tuic":
		return n.UpdateTUICUsers(tag, mapUsers(users, func(u User) option.TUICUser {
			return option.TUICUser{Name: u.Name, UUID: u.UUID, Password: u.Password}
		}))
	default:
		return fmt.Errorf("nodecore: unsupported protocol %q", protocol)
	}
}

func mapUsers[T any](users []User, convert func(User) T) []T {
	out := make([]T, len(users))
	for i, u := range users {
		out[i] = convert(u)
	}
	return out
}

// runningInbound looks up the inbound tagged tag and asserts it is the forked
// type T for protocol.
func runningInbound[T any](n *Node, tag, protocol string) (T, error) {
	var zero T
	raw, loaded := n.box.Inbound().Get(tag)
	if !loaded {
		return zero, fmt.Errorf("nodecore: no running inbound tagged %q", tag)
	}
	in, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("nodecore: inbound %q is not a %s inbound", tag, protocol)
	}
	return in, nil
}

// UpdateVLESSUsers is UpdateUsers for a VLESS inbound - the entire reason
// internal/nodecore/vless exists. Returns an error if no VLESS inbound with
// that tag is currently running.
func (n *Node) UpdateVLESSUsers(tag string, users []option.VLESSUser) error {
	in, err := runningInbound[*forkedvless.Inbound](n, tag, "VLESS")
	if err != nil {
		return err
	}
	in.UpdateUsers(users)
	return nil
}

// UpdateVMessUsers is UpdateUsers for a VMess inbound.
func (n *Node) UpdateVMessUsers(tag string, users []option.VMessUser) error {
	in, err := runningInbound[*forkedvmess.Inbound](n, tag, "VMess")
	if err != nil {
		return err
	}
	return in.UpdateUsers(users)
}

// UpdateTrojanUsers is UpdateUsers for a Trojan inbound. It fails, changing
// nothing, if two users share a password.
func (n *Node) UpdateTrojanUsers(tag string, users []option.TrojanUser) error {
	in, err := runningInbound[*forkedtrojan.Inbound](n, tag, "Trojan")
	if err != nil {
		return err
	}
	return in.UpdateUsers(users)
}

// UpdateShadowsocksUsers is UpdateUsers for a Shadowsocks inbound. The cipher
// is fixed when the inbound is built; only the users change.
func (n *Node) UpdateShadowsocksUsers(tag string, users []option.ShadowsocksUser) error {
	in, err := runningInbound[*forkedshadowsocks.Inbound](n, tag, "Shadowsocks")
	if err != nil {
		return err
	}
	return in.UpdateUsers(users)
}

// UpdateHysteria2Users is UpdateUsers for a Hysteria2 inbound.
func (n *Node) UpdateHysteria2Users(tag string, users []option.Hysteria2User) error {
	in, err := runningInbound[*forkedhysteria2.Inbound](n, tag, "Hysteria2")
	if err != nil {
		return err
	}
	in.UpdateUsers(users)
	return nil
}

// UpdateTUICUsers is UpdateUsers for a TUIC inbound. It fails, changing
// nothing, if a user's UUID is missing or malformed.
func (n *Node) UpdateTUICUsers(tag string, users []option.TUICUser) error {
	in, err := runningInbound[*forkedtuic.Inbound](n, tag, "TUIC")
	if err != nil {
		return err
	}
	return in.UpdateUsers(users)
}
