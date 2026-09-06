package nodecore

import (
	"context"
	"fmt"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/option"

	forkedvless "github.com/legendary1205/rapido-go/internal/nodecore/vless"
)

// Node wraps a running sing-box instance built from the restricted
// registries in registry.go.
type Node struct {
	box *box.Box
}

// New builds a sing-box instance from the given options - it does not
// initialize or start anything yet, call Start for that. (Box.Start does
// its own internal pre-start pass; calling Box.PreStart here too would run
// it twice and panic on a double-started component.)
func New(ctx context.Context, opts option.Options) (*Node, error) {
	ctx = box.Context(ctx, InboundRegistry(), OutboundRegistry(), EndpointRegistry(), DNSTransportRegistry(), ServiceRegistry(), CertificateProviderRegistry())
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: opts,
	})
	if err != nil {
		return nil, fmt.Errorf("nodecore: build instance: %w", err)
	}
	return &Node{box: instance}, nil
}

// Start initializes every component and opens every configured inbound's
// listener.
func (n *Node) Start() error {
	return n.box.Start()
}

// Close tears down every inbound, outbound and background service.
func (n *Node) Close() error {
	return n.box.Close()
}

// UpdateVLESSUsers replaces the user list on an already-running VLESS
// inbound identified by tag, with no listener restart and no impact on
// connections already established under other users on the same inbound -
// the entire reason internal/nodecore/vless exists. Returns an error if no
// VLESS inbound with that tag is currently running.
func (n *Node) UpdateVLESSUsers(tag string, users []option.VLESSUser) error {
	raw, loaded := n.box.Inbound().Get(tag)
	if !loaded {
		return fmt.Errorf("nodecore: no running inbound tagged %q", tag)
	}
	vlessInbound, ok := raw.(*forkedvless.Inbound)
	if !ok {
		return fmt.Errorf("nodecore: inbound %q is not a VLESS inbound", tag)
	}
	vlessInbound.UpdateUsers(users)
	return nil
}
