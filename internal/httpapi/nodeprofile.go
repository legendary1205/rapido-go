package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// coreOverridesDTO is nodes.core_overrides: keys applied on top of the fleet
// core config for one node only. Every key is optional; an absent key leaves
// the fleet value alone.
type coreOverridesDTO struct {
	LogLevel     *string `json:"log_level,omitempty"`
	SniffEnabled *bool   `json:"sniff_enabled,omitempty"`
	// DNSServers replaces the fleet list. A pointer, so that an explicit empty
	// list ("this node has no DNS servers") is told apart from an absent key.
	DNSServers *[]dnsServerDTO `json:"dns_servers,omitempty"`
	// Outbounds merge by tag: the same tag replaces the fleet outbound in
	// place, a new tag is appended. A tag can never remove one.
	Outbounds []outboundDTO `json:"outbounds,omitempty"`
	// RoutingRulesFirst go before the fleet's rules, so a node can shadow
	// specific ports or inbounds without touching what everyone else routes.
	RoutingRulesFirst []routingRuleDTO `json:"routing_rules_first,omitempty"`
}

func (o coreOverridesDTO) isZero() bool {
	return o.LogLevel == nil && o.SniffEnabled == nil && o.DNSServers == nil &&
		len(o.Outbounds) == 0 && len(o.RoutingRulesFirst) == 0
}

// parseCoreOverrides decodes a core_overrides value strictly: an unknown key
// is an error, because a typo that is silently ignored leaves the node
// running the fleet setting the admin believes they replaced.
func parseCoreOverrides(raw []byte) (coreOverridesDTO, error) {
	var out coreOverridesDTO
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return out, nil
	}
	if trimmed[0] != '{' {
		return out, errors.New("core_overrides must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return coreOverridesDTO{}, fmt.Errorf("core_overrides: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return coreOverridesDTO{}, errors.New("core_overrides: unexpected data after the JSON object")
	}
	return out, nil
}

// apply returns the fleet core config with these overrides on top. The fleet
// value is never modified: its slices are shared by every node's payload.
func (o coreOverridesDTO) apply(fleet coreConfigDTO) coreConfigDTO {
	if o.isZero() {
		return fleet
	}
	out := fleet
	if o.LogLevel != nil {
		out.LogLevel = *o.LogLevel
	}
	if o.SniffEnabled != nil {
		out.SniffEnabled = *o.SniffEnabled
	}
	if o.DNSServers != nil {
		out.DNSServers = append(make([]dnsServerDTO, 0, len(*o.DNSServers)), *o.DNSServers...)
	}
	if len(o.Outbounds) > 0 {
		merged := make([]outboundDTO, 0, len(fleet.Outbounds)+len(o.Outbounds))
		merged = append(merged, fleet.Outbounds...)
		at := make(map[string]int, len(merged))
		for i, ob := range merged {
			at[ob.Tag] = i
		}
		for _, ob := range o.Outbounds {
			if i, ok := at[ob.Tag]; ok {
				merged[i] = ob
				continue
			}
			at[ob.Tag] = len(merged)
			merged = append(merged, ob)
		}
		out.Outbounds = merged
	}
	if len(o.RoutingRulesFirst) > 0 {
		rules := make([]routingRuleDTO, 0, len(o.RoutingRulesFirst)+len(fleet.RoutingRules))
		rules = append(rules, o.RoutingRulesFirst...)
		out.RoutingRules = append(rules, fleet.RoutingRules...)
	}
	return out
}

// validateCoreOverrides checks that o is well formed AND that the config it
// produces for a node passes exactly the validation the fleet config passes
// on save. knownInbounds are the real inbound tags, for the rules o adds;
// nil skips that check.
func validateCoreOverrides(fleet coreConfigDTO, o coreOverridesDTO, knownInbounds map[string]bool) string {
	if o.LogLevel != nil && !validLogLevels[*o.LogLevel] {
		return "core_overrides: invalid log_level"
	}
	seen := make(map[string]bool, len(o.Outbounds))
	for _, ob := range o.Outbounds {
		if seen[ob.Tag] {
			return "core_overrides: duplicate outbound tag: " + ob.Tag
		}
		seen[ob.Tag] = true
	}
	if msg := validateCoreConfig(o.apply(fleet)); msg != "" {
		return "core_overrides: " + msg
	}
	for _, rule := range o.RoutingRulesFirst {
		for _, tag := range rule.Inbound {
			if knownInbounds != nil && !knownInbounds[tag] {
				return "core_overrides: routing rule targets unknown inbound: " + tag
			}
		}
	}
	return ""
}

// nodeProfile is everything that makes one node's config differ from the
// fleet default. Nodes whose profiles are equal share one cached payload.
type nodeProfile struct {
	InboundTags   []string // empty = every inbound
	ListenPorts   []int32  // empty = every port of the served inbounds
	CoreOverrides coreOverridesDTO
}

func profileFromNode(n generated.Node) (nodeProfile, error) {
	ov, err := parseCoreOverrides(n.CoreOverrides)
	if err != nil {
		return nodeProfile{}, err
	}
	return nodeProfile{InboundTags: n.InboundTags, ListenPorts: n.ListenPorts, CoreOverrides: ov}, nil
}

func (p nodeProfile) isDefault() bool {
	return len(p.InboundTags) == 0 && len(p.ListenPorts) == 0 && p.CoreOverrides.isZero()
}

// key identifies the profile for caching. Order and repeats in the tag and
// port lists do not change what a node is served, so they do not change the key.
func (p nodeProfile) key() string {
	if p.isDefault() {
		return "default"
	}
	tags := append([]string(nil), p.InboundTags...)
	sort.Strings(tags)
	ports := append([]int32(nil), p.ListenPorts...)
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	raw, _ := json.Marshal(struct {
		Tags  []string         `json:"t"`
		Ports []int32          `json:"p"`
		Core  coreOverridesDTO `json:"c"`
	}{tags, ports, p.CoreOverrides})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// specPorts is every port an inbound listens on, ascending.
func specPorts(in nodeConfigInboundSpec) []uint16 {
	if len(in.ListenPorts) > 0 {
		return in.ListenPorts
	}
	return []uint16{in.ListenPort}
}

// filterInbounds narrows the fleet's inbounds to what this node serves. The
// default profile gets the fleet slice itself, untouched, so its payload is
// byte-for-byte what it was before profiles existed.
func (p nodeProfile) filterInbounds(fleet []nodeConfigInboundSpec) []nodeConfigInboundSpec {
	if len(p.InboundTags) == 0 && len(p.ListenPorts) == 0 {
		return fleet
	}
	var tags map[string]bool
	if len(p.InboundTags) > 0 {
		tags = make(map[string]bool, len(p.InboundTags))
		for _, t := range p.InboundTags {
			tags[t] = true
		}
	}
	var ports map[uint16]bool
	if len(p.ListenPorts) > 0 {
		ports = make(map[uint16]bool, len(p.ListenPorts))
		for _, port := range p.ListenPorts {
			ports[uint16(port)] = true
		}
	}

	out := make([]nodeConfigInboundSpec, 0, len(fleet))
	for _, in := range fleet {
		if tags != nil && !tags[in.Tag] {
			continue
		}
		if ports == nil {
			out = append(out, in)
			continue
		}
		all := specPorts(in)
		kept := make([]uint16, 0, len(all))
		for _, port := range all {
			if ports[port] {
				kept = append(kept, port)
			}
		}
		if len(kept) == 0 {
			continue // none of this inbound's ports are ones the node serves
		}
		if len(kept) != len(all) {
			// ListenPort is what a node that predates listen_ports reads, so
			// it has to stay one of the ports actually served.
			primaryKept := false
			for _, port := range kept {
				if port == in.ListenPort {
					primaryKept = true
				}
			}
			if !primaryKept {
				in.ListenPort = kept[0]
			}
			in.ListenPorts = nil
			if len(kept) > 1 {
				in.ListenPorts = kept
			}
		}
		out = append(out, in)
	}
	return out
}

// nodeProfileFields are the three stored profile columns in the shape the
// update handler overlays request fields onto.
type nodeProfileFields struct {
	tags      []string
	ports     []int32
	overrides []byte // canonical JSON, '{}' when there are none
}

var emptyCoreOverridesJSON = []byte("{}")

func profileFieldsFromNode(n generated.Node) nodeProfileFields {
	f := nodeProfileFields{tags: n.InboundTags, ports: n.ListenPorts, overrides: n.CoreOverrides}
	if len(f.overrides) == 0 {
		f.overrides = emptyCoreOverridesJSON
	}
	return f
}

// patchField is a request field that is absent, null, or a value - the
// difference between "leave this alone" and "clear it".
type patchField[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (p *patchField[T]) UnmarshalJSON(b []byte) error { return p.decode(b, "") }

// decode is UnmarshalJSON with a message of the caller's choosing for a value
// of the wrong type: the decoder's own error names no field.
func (p *patchField[T]) decode(b []byte, wrongType string) error {
	p.Set = true
	if string(bytes.TrimSpace(b)) == "null" {
		p.Null = true
		return nil
	}
	if err := json.Unmarshal(b, &p.Value); err != nil {
		if wrongType != "" {
			return errors.New(wrongType)
		}
		return err
	}
	return nil
}

type tagsPatch struct{ patchField[[]string] }

func (p *tagsPatch) UnmarshalJSON(b []byte) error {
	return p.decode(b, "inbound_tags must be an array of strings")
}

type portsPatch struct{ patchField[[]int] }

func (p *portsPatch) UnmarshalJSON(b []byte) error {
	return p.decode(b, "listen_ports must be an array of integers")
}

// resolveNodeProfile overlays the fields a request actually sent onto base,
// validating only what was sent: an unrelated edit of a node must not start
// failing because the fleet moved on since its profile was saved. The string
// result is a validation message (empty when fine); the error is an internal
// failure. Empty and null both clear a field back to the default.
func (h *Handler) resolveNodeProfile(ctx context.Context, base nodeProfileFields,
	tagsIn tagsPatch, portsIn portsPatch, overridesIn patchField[json.RawMessage]) (nodeProfileFields, string, error) {
	out := base

	var known map[string]bool
	knownInbounds := func() (map[string]bool, error) {
		if known != nil {
			return known, nil
		}
		tags, err := h.store.Queries.ListInboundTags(ctx)
		if err != nil {
			return nil, err
		}
		known = make(map[string]bool, len(tags))
		for _, t := range tags {
			known[t] = true
		}
		return known, nil
	}

	if tagsIn.Set {
		out.tags = nil
		if !tagsIn.Null && len(tagsIn.Value) > 0 {
			inbounds, err := knownInbounds()
			if err != nil {
				return out, "", err
			}
			seen := make(map[string]bool, len(tagsIn.Value))
			for _, raw := range tagsIn.Value {
				tag := strings.TrimSpace(raw)
				if tag == "" {
					return out, "inbound_tags: a tag must not be empty", nil
				}
				if seen[tag] {
					return out, "inbound_tags: duplicate tag " + tag, nil
				}
				if !inbounds[tag] {
					return out, "inbound_tags: unknown inbound " + tag, nil
				}
				seen[tag] = true
				out.tags = append(out.tags, tag)
			}
			sort.Strings(out.tags)
		}
	}

	if portsIn.Set {
		out.ports = nil
		if !portsIn.Null && len(portsIn.Value) > 0 {
			seen := make(map[int]bool, len(portsIn.Value))
			for _, port := range portsIn.Value {
				if port < 1 || port > 65535 {
					return out, fmt.Sprintf("listen_ports: invalid port %d (must be 1-65535)", port), nil
				}
				if seen[port] {
					return out, fmt.Sprintf("listen_ports: duplicate port %d", port), nil
				}
				seen[port] = true
				out.ports = append(out.ports, int32(port))
			}
			sort.Slice(out.ports, func(i, j int) bool { return out.ports[i] < out.ports[j] })
		}
	}

	if overridesIn.Set {
		out.overrides = emptyCoreOverridesJSON
		if !overridesIn.Null {
			ov, err := parseCoreOverrides(overridesIn.Value)
			if err != nil {
				return out, err.Error(), nil
			}
			if !ov.isZero() {
				inbounds, err := knownInbounds()
				if err != nil {
					return out, "", err
				}
				fleetRow, err := h.store.Queries.GetCoreConfig(ctx)
				if err != nil {
					return out, "", err
				}
				if msg := validateCoreOverrides(toCoreConfigDTO(fleetRow), ov, inbounds); msg != "" {
					return out, msg, nil
				}
				raw, err := json.Marshal(ov)
				if err != nil {
					return out, "", err
				}
				out.overrides = raw
			}
		}
	}
	return out, "", nil
}

// validateNodeOverridesAgainstFleet is the other direction of the merge
// check: saving a fleet core config must not leave any node with overrides
// that no longer produce a valid config (e.g. a routing rule of that node
// pointing at an outbound the fleet just deleted).
func (h *Handler) validateNodeOverridesAgainstFleet(ctx context.Context, fleet coreConfigDTO) (string, error) {
	nodes, err := h.store.Queries.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		ov, err := parseCoreOverrides(n.CoreOverrides)
		if err != nil || ov.isZero() {
			continue
		}
		// Inbound references are checked when the overrides are saved; only
		// the outbound side depends on the fleet config being replaced here.
		if msg := validateCoreOverrides(fleet, ov, nil); msg != "" {
			return "node " + n.Name + ": " + msg, nil
		}
	}
	return "", nil
}
