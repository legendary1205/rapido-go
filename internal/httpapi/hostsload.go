package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/loadmap"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
	"github.com/legendary1205/rapido-go/internal/subscription"
)

// loadInventory feeds the load map the nodes and enabled hosts it maps between.
type loadInventory struct {
	q *generated.Queries
}

func (i loadInventory) Nodes(ctx context.Context) ([]loadmap.Node, error) {
	rows, err := i.q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]loadmap.Node, len(rows))
	for k, n := range rows {
		out[k] = loadmap.Node{ID: n.ID, Address: n.Address, Status: n.Status, InboundTags: n.InboundTags, ListenPorts: n.ListenPorts}
	}
	return out, nil
}

// Hosts returns every enabled host in display (priority, id) order, the same
// set a subscription is built from.
func (i loadInventory) Hosts(ctx context.Context) ([]loadmap.Host, error) {
	rows, err := i.q.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]loadmap.Host, 0, len(rows))
	for _, r := range rows {
		if r.IsDisabled.Valid && r.IsDisabled.Bool {
			continue
		}
		host := loadmap.Host{ID: r.ID, Remark: r.Remark, Address: r.Address, InboundTag: r.InboundTag}
		if r.Port.Valid {
			host.Port = int(r.Port.Int32)
		}
		out = append(out, host)
	}
	return out, nil
}

// newLoadMap wires the load map to this panel's Postgres and Redis. It returns
// nil when there is no Redis client to read presence from (the feature then
// stays off and every caller treats a nil map as "no data").
func newLoadMap(store *Store, publicIP string, capacity int, logger *slog.Logger) *loadmap.Map {
	if store == nil || store.Queries == nil || store.Cache == nil {
		return nil
	}
	return loadmap.New(loadmap.Config{
		Capacity: capacity,
		Logger:   logger,
		// The panel's own {SERVER_IP} is the one address variable that means
		// the same thing for every user, so it can be resolved; anything else
		// (e.g. {USERNAME}) never is.
		ExpandAddress: func(address string) string {
			if publicIP != "" {
				return strings.ReplaceAll(address, "{SERVER_IP}", publicIP)
			}
			return address
		},
	}, loadInventory{q: store.Queries}, loadmap.NewRedisPresence(store.Cache.Raw()))
}

// WithConfigLoad applies the CONFIG_LOAD_INDICATOR / CONFIG_LOAD_CAPACITY /
// CONFIG_SORT_BY_LOAD settings: whether plain remarks get the ` {LOAD}` suffix
// appended, how many concurrent connections read as 100%, and whether a
// user's own configs are ordered least-loaded first. A setter rather than more
// NewHandler parameters, like WithSubscriptionURLPrefixes.
func (h *Handler) WithConfigLoad(indicator bool, capacity int, sortByLoad bool) *Handler {
	if capacity < 1 {
		capacity = loadmap.DefaultCapacity
	}
	h.loadIndicator = indicator
	h.loadCapacity = capacity
	h.sortByLoad = sortByLoad
	h.loads = newLoadMap(h.store, h.publicIP, capacity, h.logger)
	return h
}

// loadSnapshot is the cached fleet load view; nil (which knows nothing) when
// the feature has no data source. Callers on the subscription path use it once
// per request.
func (h *Handler) loadSnapshot(ctx context.Context) *loadmap.Snapshot {
	if h.loads == nil {
		return nil
	}
	return h.loads.Snapshot(ctx)
}

// requestLoad hands a request's hosts the load snapshot, fetched at most once
// and only if something in the request can use it: the indicator or sorting is
// on, or a remark template asks for {LOAD...} itself. A panel with all of that
// off never touches the load map on the subscription path.
type requestLoad struct {
	h       *Handler
	ctx     context.Context
	snap    *loadmap.Snapshot
	fetched bool
}

func (r *requestLoad) get() *loadmap.Snapshot {
	if !r.fetched {
		r.fetched = true
		r.snap = r.h.loadSnapshot(r.ctx)
	}
	return r.snap
}

// remark renders one local host's remark for the current user. vars is the
// request's shared variable map, PROTOCOL/TRANSPORT already set; its {LOAD*}
// values are (re)set here on every call. sortKey is the host's percent for
// ordering (unknown load ranks after everything else) and sortable is false
// for hosts that keep their place: info pseudo-hosts, and hosts the snapshot
// does not know yet.
func (r *requestLoad) remark(vars subscription.Variables, host generated.Host) (remark string, sortKey int, sortable bool) {
	template := host.Remark
	usesLoad := strings.Contains(template, "{LOAD")
	if !r.h.loadIndicator && !r.h.sortByLoad && !usesLoad {
		vars.SetLoad("", "", "")
		return vars.Format(template), 0, false
	}

	e := r.get().For(host.ID)
	sortKey = unknownLoadSortKey
	if e != nil && e.Known {
		vars.SetLoad(e.Level.Emoji(), strconv.Itoa(e.Percent)+"%", string(e.Level))
		sortKey = e.Percent
		// e is only ever set for a plain remark or one that already uses a
		// {LOAD...} variable (info hosts are not in the snapshot), so "does
		// not use it" here means "has no variable at all".
		if r.h.loadIndicator && !e.UsesLoad {
			if template == "" {
				template = "{LOAD}"
			} else {
				// A remark often ends in a space of its own; one separator is enough.
				template = strings.TrimRight(template, " ") + subscription.AutoLoadSuffix
			}
		}
	} else {
		vars.SetLoad("", "", "")
	}
	return vars.FormatRemark(template), sortKey, e != nil
}

// unknownLoadSortKey ranks a config without load data after every known one
// (a known percent never exceeds 100).
const unknownLoadSortKey = 101

// peerRemark is a gateway peer host's remark: no load suffix, and a {LOAD...}
// variable in the peer's template renders as nothing.
func peerRemark(vars subscription.Variables, peerName, template string) string {
	vars.SetLoad("", "", "")
	return "[" + peerName + "] " + vars.FormatRemark(template)
}

// pendingHost is one local host held back so the whole set can be ordered by
// load before it reaches the caller.
type pendingHost struct {
	protocol string
	settings proxysettings.Settings
	remark   string
	address  string
	eff      subscription.EffectiveInbound
	sortKey  int
	sortable bool
}

// sortPendingByLoad orders the sortable hosts by ascending load (ties keep
// their incoming, priority order) and puts them back into the slots the
// sortable hosts occupied; every other host stays exactly where it is.
func sortPendingByLoad(items []pendingHost) {
	var slots []int
	for i, it := range items {
		if it.sortable {
			slots = append(slots, i)
		}
	}
	if len(slots) < 2 {
		return
	}
	moved := make([]pendingHost, len(slots))
	for k, i := range slots {
		moved[k] = items[i]
	}
	sort.SliceStable(moved, func(a, b int) bool { return moved[a].sortKey < moved[b].sortKey })
	for k, i := range slots {
		items[i] = moved[k]
	}
}

type hostsLoadResponse struct {
	Capacity   int             `json:"capacity"`
	Indicator  bool            `json:"indicator"`
	SortByLoad bool            `json:"sort_by_load"`
	UpdatedAt  string          `json:"updated_at"`
	Hosts      []hostLoadEntry `json:"hosts"`
}

// hostLoadEntry is one config's live load. A host nothing is reporting for has
// conns/percent null and level "unknown".
type hostLoadEntry struct {
	HostID  int32   `json:"host_id"`
	Remark  string  `json:"remark"`
	Address string  `json:"address"`
	Port    *int    `json:"port"`
	Conns   *int    `json:"conns"`
	Percent *int    `json:"percent"`
	Level   string  `json:"level"`
	NodeIDs []int32 `json:"node_ids"`
}

// handleGetHostsLoad implements GET /api/hosts/load (sudo only): the live load
// of every enabled config (info hosts excluded), from the same cached snapshot
// subscriptions are rendered with.
func (h *Handler) handleGetHostsLoad(c *gin.Context) {
	snap := h.loadSnapshot(c.Request.Context())

	capacity := h.loadCapacity
	if capacity < 1 {
		capacity = loadmap.DefaultCapacity
	}
	resp := hostsLoadResponse{
		Capacity: capacity, Indicator: h.loadIndicator, SortByLoad: h.sortByLoad,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339), Hosts: []hostLoadEntry{},
	}
	if snap != nil {
		resp.UpdatedAt = snap.At.UTC().Format(time.RFC3339)
		for _, hl := range snap.Hosts {
			entry := hostLoadEntry{
				HostID: hl.HostID, Remark: hl.Remark, Address: hl.Address,
				Level: string(hl.Level), NodeIDs: []int32{},
			}
			if hl.Port > 0 {
				port := hl.Port
				entry.Port = &port
			}
			if hl.Known {
				conns, percent := hl.Conns, hl.Percent
				entry.Conns, entry.Percent = &conns, &percent
				entry.NodeIDs = hl.NodeIDs
			}
			resp.Hosts = append(resp.Hosts, entry)
		}
	}
	c.JSON(http.StatusOK, resp)
}
