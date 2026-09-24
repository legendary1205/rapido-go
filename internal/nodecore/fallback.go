package nodecore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// FallbackGroup describes one exit that must keep working when its WireGuard
// tunnel dies. Group is a selector outbound with two members: Primary, bound
// to Interface, and Fallback, the same exit over the default route. Routing
// rules point at Group and never notice which member is live.
type FallbackGroup struct {
	Group     string
	Primary   string
	Fallback  string
	Interface string
}

// Health answers whether a tunnel is usable right now.
type Health interface {
	Up(iface string) bool
}

type selectorGroup interface {
	SelectOutbound(tag string) bool
	Now() string
}

func (n *Node) selectorGroup(group string) (selectorGroup, error) {
	raw, loaded := n.box.Outbound().Outbound(group)
	if !loaded {
		return nil, fmt.Errorf("nodecore: no outbound tagged %q", group)
	}
	sel, ok := raw.(selectorGroup)
	if !ok {
		return nil, fmt.Errorf("nodecore: outbound %q is not a selector", group)
	}
	return sel, nil
}

// SelectOutbound points a selector outbound at one of its members.
func (n *Node) SelectOutbound(group, member string) error {
	sel, err := n.selectorGroup(group)
	if err != nil {
		return err
	}
	if !sel.SelectOutbound(member) {
		return fmt.Errorf("nodecore: selector %q has no member %q", group, member)
	}
	return nil
}

// SelectedOutbound returns the member a selector currently routes to.
func (n *Node) SelectedOutbound(group string) (string, error) {
	sel, err := n.selectorGroup(group)
	if err != nil {
		return "", err
	}
	return sel.Now(), nil
}

// Supervisor keeps every fallback group pointed at its Primary while the
// tunnel is healthy and at its Fallback while it is not.
type Supervisor struct {
	node   *Node
	groups []FallbackGroup
	health Health
	logger *slog.Logger

	// Foreign, when set, reports interfaces that do not belong to this host.
	// Every node is sent every exit in the fleet, so most groups here guard a
	// tunnel that lives on another server; those are switched silently, since
	// "tunnel is down" about a tunnel this host never had is noise.
	Foreign func(iface string) bool

	mu     sync.RWMutex
	active map[string]bool
}

func NewSupervisor(node *Node, groups []FallbackGroup, health Health, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{node: node, groups: groups, health: health, logger: logger, active: make(map[string]bool)}
}

// Reconcile makes one pass over every group.
func (s *Supervisor) Reconcile() {
	for _, g := range s.groups {
		want := g.Primary
		if !s.health.Up(g.Interface) {
			want = g.Fallback
		}
		current, err := s.node.SelectedOutbound(g.Group)
		if err != nil {
			s.logger.Error("fallback: read selection", "group", g.Group, "error", err)
			continue
		}
		if current != want {
			if err := s.node.SelectOutbound(g.Group, want); err != nil {
				s.logger.Error("fallback: switch", "group", g.Group, "to", want, "error", err)
				continue
			}
			if s.Foreign != nil && s.Foreign(g.Interface) {
				// nothing to report about someone else's tunnel
			} else if want == g.Fallback {
				s.logger.Warn("fallback: tunnel is down, using a direct connection", "exit", g.Group, "tunnel", g.Interface)
			} else {
				s.logger.Info("fallback: tunnel is back, using it again", "exit", g.Group, "tunnel", g.Interface)
			}
		}
		s.setActive(g.Interface, g.Group, want == g.Fallback)
	}
}

func (s *Supervisor) setActive(iface, group string, fallback bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// keyed by interface AND group: two exits may share a tunnel, and the
	// tunnel is on fallback while any of them is
	s.active[iface+"\x00"+group] = fallback
}

// ActiveByInterface reports which tunnels currently have at least one exit
// running over the fallback.
func (s *Supervisor) ActiveByInterface() map[string]bool {
	out := make(map[string]bool)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.groups {
		if s.active[g.Interface+"\x00"+g.Group] {
			out[g.Interface] = true
		}
	}
	return out
}

// Run reconciles immediately and then every interval until ctx is done.
func (s *Supervisor) Run(ctx context.Context, interval time.Duration) {
	s.Reconcile()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reconcile()
		}
	}
}
