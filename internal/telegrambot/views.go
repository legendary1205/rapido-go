package telegrambot

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

func btn(text, data string) button { return button{Text: text, Data: data} }

// navRow is the Back/Home row every screen ends with.
func (r *request) navRow(back string) []button {
	row := make([]button, 0, 2)
	if back != "" {
		row = append(row, btn(r.t("btn.back"), back))
	}
	return append(row, btn(r.t("btn.home"), "h"))
}

func (r *request) errorScreen(msgHTML, back string) screen {
	return screen{Text: "❌ " + msgHTML, KB: keyboard{r.navRow(back)}}
}

// failure turns a refused or failed API call into a readable screen. For user
// routes a missing user and someone else's user read the same, so a scoped admin
// cannot probe which usernames exist.
func (r *request) failure(res apiResult, err error, back string, userScope bool) screen {
	switch {
	case err != nil:
		r.c.d.Logger.Warn("telegram console: api call failed", "error", err)
		return r.errorScreen(r.t("err.generic"), back)
	case res.Status == http.StatusUnauthorized:
		return r.errorScreen(r.t("err.auth"), back)
	case userScope && (res.Status == http.StatusNotFound || res.Status == http.StatusForbidden):
		return r.errorScreen(r.t("err.usernotfound"), back)
	case res.Status == http.StatusForbidden:
		return r.errorScreen(r.t("err.forbidden"), back)
	}
	if d := res.detail(); d != "" {
		return r.errorScreen(escapeHTML(d), back)
	}
	return r.errorScreen(r.t("err.generic"), back)
}

func statusEmoji(status string) string {
	switch status {
	case "active":
		return "🟢"
	case "disabled":
		return "🔴"
	case "limited":
		return "🟠"
	case "expired":
		return "⌛"
	case "on_hold":
		return "⏸"
	}
	return "⚪"
}

func (r *request) statusLabel(status string) string {
	key := "st." + status
	if !hasKey(key) {
		return escapeHTML(status)
	}
	return r.t(key)
}

func (r *request) roleLabel() string {
	if r.who.IsSudo {
		return r.t("role.sudo")
	}
	return r.t("role.admin")
}

// ---- home, help, language ----

func (r *request) home() {
	rows := keyboard{
		{btn(r.t("btn.system"), "sy"), btn(r.t("btn.users"), "us")},
	}
	if r.who.IsSudo {
		rows = append(rows,
			[]button{btn(r.t("btn.newuser"), "nu"), btn(r.t("btn.nodes"), "nd")},
			[]button{btn(r.t("btn.backup"), "bk"), btn(r.t("btn.help"), "hp")},
		)
	} else {
		rows = append(rows, []button{btn(r.t("btn.newuser"), "nu"), btn(r.t("btn.help"), "hp")})
	}
	rows = append(rows, []button{btn(r.t("btn.lang"), "lg")})
	r.show(screen{
		Text: r.t("home.title", escapeHTML(r.who.Username), r.roleLabel()),
		KB:   rows,
	})
}

func (r *request) help() {
	text := r.t("help.body")
	if r.who.IsSudo {
		text += "\n" + r.t("help.sudo")
	}
	r.show(screen{Text: text, KB: keyboard{r.navRow("")}})
}

func (r *request) toggleLang() {
	if r.lang == langFA {
		r.lang = langEN
	} else {
		r.lang = langFA
	}
	r.c.saveLang(r.ctx, r.uid, r.lang)
	r.home()
}

// ---- system ----

func (r *request) system() {
	res, err := r.call(http.MethodGet, "/api/system", nil)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "h", false))
		return
	}
	var s systemStats
	if err := res.decode(&s); err != nil {
		r.show(r.failure(res, err, "h", false))
		return
	}

	var b strings.Builder
	b.WriteString(r.t("sys.title"))
	if !r.who.IsSudo {
		b.WriteString(" " + r.t("sys.own_only"))
	}
	b.WriteString("\n\n")
	b.WriteString(r.t("sys.users", s.TotalUser) + "\n")
	b.WriteString(r.t("sys.by_status", s.UsersActive, s.UsersDisabled, s.UsersLimited, s.UsersExpired, s.UsersOnHold) + "\n")
	b.WriteString(r.t("sys.online", s.OnlineUsers) + "\n")
	if r.who.IsSudo {
		b.WriteString(r.t("sys.traffic", formatBytes(s.IncomingBandwidth), formatBytes(s.OutgoingBandwidth)) + "\n")
		if s.IncomingBandwidthSpeed > 0 || s.OutgoingBandwidthSpeed > 0 {
			b.WriteString(r.t("sys.speed", formatBytes(s.IncomingBandwidthSpeed), formatBytes(s.OutgoingBandwidthSpeed)) + "\n")
		}
		b.WriteString(r.panelAndNodesSummary(s))
	}
	r.show(screen{Text: strings.TrimRight(b.String(), "\n"), KB: keyboard{
		{btn(r.t("btn.refresh"), "sy")},
		r.navRow(""),
	}})
}

// panelAndNodesSummary is the sudo-only tail of the System screen. A failure in
// either lookup just drops that line: the user totals above stand on their own.
func (r *request) panelAndNodesSummary(s systemStats) string {
	var b strings.Builder
	if s.MemTotal > 0 {
		b.WriteString("\n" + r.t("sys.panel", fmt.Sprintf("%.0f%%", s.CPUUsage), formatBytes(s.MemUsed), formatBytes(s.MemTotal)) + "\n")
	}
	nodes, nres, nerr := r.listNodes()
	if nerr != nil || !nres.ok() {
		return b.String()
	}
	connected := 0
	for _, n := range nodes {
		if n.Status == "connected" {
			connected++
		}
	}
	b.WriteString(r.t("sys.nodes", connected, len(nodes)) + "\n")
	if mon, ok := r.monitoring(); ok {
		down := 0
		for _, h := range mon.Hosts {
			for _, t := range h.Tunnels {
				if !t.Up || !t.Present {
					down++
				}
			}
		}
		if down > 0 {
			b.WriteString(r.t("sys.tunnels_bad", down) + "\n")
		} else if len(mon.Hosts) > 0 {
			b.WriteString(r.t("sys.tunnels_ok") + "\n")
		}
	}
	return b.String()
}

func (r *request) listNodes() ([]nodeDTO, apiResult, error) {
	res, err := r.call(http.MethodGet, "/api/nodes", nil)
	if err != nil || !res.ok() {
		return nil, res, err
	}
	var nodes []nodeDTO
	return nodes, res, res.decode(&nodes)
}

func (r *request) monitoring() (monitoringDTO, bool) {
	res, err := r.call(http.MethodGet, "/api/monitoring", nil)
	if err != nil || !res.ok() {
		return monitoringDTO{}, false
	}
	var m monitoringDTO
	return m, res.decode(&m) == nil
}

// ---- nodes ----

func (r *request) nodes() {
	nodes, res, err := r.listNodes()
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "h", false))
		return
	}
	mon, hasMon := r.monitoring()
	hostByNode := map[int32]hostDTO{}
	var panelHost *hostDTO
	if hasMon {
		for i := range mon.Hosts {
			h := mon.Hosts[i]
			if h.NodeID != nil {
				hostByNode[*h.NodeID] = h
			} else {
				panelHost = &mon.Hosts[i]
			}
		}
	}

	var b strings.Builder
	b.WriteString(r.t("nodes.title") + "\n")
	if panelHost != nil && panelHost.HasMetrics {
		b.WriteString("\n" + r.t("nodes.panel_line") + " " + r.hostLine(*panelHost) + "\n")
		if line := r.tunnelLine(panelHost.Tunnels); line != "" {
			b.WriteString(line + "\n")
		}
	}
	if len(nodes) == 0 {
		b.WriteString("\n" + r.t("nodes.none"))
	}
	shown := 0
	for _, n := range nodes {
		var blk strings.Builder
		blk.WriteString("\n" + nodeEmoji(n.Status) + " <b>" + escapeHTML(truncateRunes(n.Name, 40)) + "</b> · <code>" +
			escapeHTML(truncateRunes(n.Address, 60)) + "</code>\n")
		blk.WriteString("   " + r.t("nodes.status", r.nodeStatus(n.Status)))
		if n.XrayVersion != nil && *n.XrayVersion != "" {
			blk.WriteString(" · xray " + escapeHTML(truncateRunes(*n.XrayVersion, 20)))
		}
		blk.WriteString("\n")
		if n.Status != "connected" && n.Message != nil && *n.Message != "" {
			blk.WriteString("   ⚠️ " + escapeHTML(truncateRunes(*n.Message, 120)) + "\n")
		}
		if h, ok := hostByNode[n.ID]; ok {
			if h.HasMetrics {
				blk.WriteString("   " + r.hostLine(h) + "\n")
			}
			if h.Stale {
				blk.WriteString("   " + r.t("nodes.stale") + "\n")
			}
			if line := r.tunnelLine(h.Tunnels); line != "" {
				blk.WriteString(line + "\n")
			}
		} else if hasMon {
			blk.WriteString("   " + r.t("nodes.nometrics") + "\n")
		}
		if utf8Len(b.String())+utf8Len(blk.String()) > maxMessageRunes-200 {
			b.WriteString("\n" + r.t("nodes.more", len(nodes)-shown))
			break
		}
		b.WriteString(blk.String())
		shown++
	}
	r.show(screen{Text: strings.TrimRight(b.String(), "\n"), KB: keyboard{
		{btn(r.t("btn.refresh"), "nd")},
		r.navRow(""),
	}})
}

func nodeEmoji(status string) string {
	switch status {
	case "connected":
		return "🟢"
	case "connecting":
		return "🟡"
	case "error":
		return "🔴"
	}
	return "⚪"
}

func (r *request) nodeStatus(status string) string {
	if key := "ns." + status; hasKey(key) {
		return r.t(key)
	}
	return escapeHTML(status)
}

func (r *request) hostLine(h hostDTO) string {
	parts := make([]string, 0, 5)
	if h.CPUPercent != nil {
		parts = append(parts, fmt.Sprintf("CPU %.0f%%", *h.CPUPercent))
	}
	if h.MemPercent != nil {
		parts = append(parts, fmt.Sprintf("RAM %.0f%%", *h.MemPercent))
	}
	if h.DiskPercent != nil {
		parts = append(parts, fmt.Sprintf("Disk %.0f%%", *h.DiskPercent))
	}
	if h.RxRate != nil && h.TxRate != nil {
		parts = append(parts, fmt.Sprintf("↓%s/s ↑%s/s", formatBytes(*h.RxRate), formatBytes(*h.TxRate)))
	}
	return strings.Join(parts, " · ")
}

// tunnelLine summarises WireGuard health: every tunnel by name with its state,
// so the one that is down is named rather than just counted.
func (r *request) tunnelLine(tunnels []tunnelDTO) string {
	if len(tunnels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		name := escapeHTML(truncateRunes(t.Name, 20))
		switch {
		case !t.Present:
			parts = append(parts, "❌ "+name+" ("+r.t("wg.missing")+")")
		case !t.Up:
			parts = append(parts, "❌ "+name+" ("+r.t("wg.down")+")")
		case t.HandshakeAgeSeconds != nil:
			parts = append(parts, "✅ "+name+" ("+shortAge(*t.HandshakeAgeSeconds)+")")
		default:
			parts = append(parts, "✅ "+name)
		}
	}
	return "   🔐 WireGuard: " + strings.Join(parts, " · ")
}

func shortAge(seconds float64) string {
	s := int(math.Max(seconds, 0))
	switch {
	case s < 90:
		return fmt.Sprintf("%ds", s)
	case s < 5400:
		return fmt.Sprintf("%dm", s/60)
	case s < 172800:
		return fmt.Sprintf("%dh", s/3600)
	}
	return fmt.Sprintf("%dd", s/86400)
}

// ---- users: menu, list, search ----

// Filter codes are short because they travel in callback data.
var filterStatus = map[string]string{
	"*": "", "ac": "active", "di": "disabled", "li": "limited", "ex": "expired", "oh": "on_hold",
}

var filterOrder = []string{"*", "ac", "di", "li", "ex", "oh"}

func (r *request) filterLabel(code string) string {
	if code == "*" {
		return r.t("flt.all")
	}
	return statusEmoji(filterStatus[code]) + " " + r.t("st."+filterStatus[code])
}

func (r *request) usersMenu() {
	counts := map[string]int64{}
	if res, err := r.call(http.MethodGet, "/api/system", nil); err == nil && res.ok() {
		var s systemStats
		if res.decode(&s) == nil {
			counts = map[string]int64{
				"*": s.TotalUser, "ac": s.UsersActive, "di": s.UsersDisabled,
				"li": s.UsersLimited, "ex": s.UsersExpired, "oh": s.UsersOnHold,
			}
		}
	}
	label := func(code string) string {
		l := r.filterLabel(code)
		if n, ok := counts[code]; ok {
			l += fmt.Sprintf(" (%d)", n)
		}
		return l
	}
	kb := keyboard{
		{btn(label("*"), "l:*:0")},
		{btn(label("ac"), "l:ac:0"), btn(label("di"), "l:di:0")},
		{btn(label("li"), "l:li:0"), btn(label("ex"), "l:ex:0")},
		{btn(label("oh"), "l:oh:0")},
		{btn(r.t("btn.newuser"), "nu")},
		r.navRow(""),
	}
	r.show(screen{Text: r.t("users.menu"), KB: kb})
}

func (r *request) listFromCallback(rest string) {
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) < 2 {
		r.usersMenu()
		return
	}
	page := 0
	fmt.Sscanf(parts[1], "%d", &page)
	if page < 0 {
		page = 0
	}
	query := ""
	if len(parts) == 3 {
		query = parts[2]
	}
	r.list(parts[0], page, query)
}

// searchQueryBytes keeps a query small enough to ride in callback data
// (`l:<filter>:<page>:<query>`) next to the prefix.
const searchQueryBytes = 40

func (r *request) list(filter string, page int, query string) {
	status, known := filterStatus[filter]
	if !known {
		filter, status = "*", ""
	}
	pg, res, err := r.c.listUsers(r.ctx, r.who, status, query, page)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", false))
		return
	}
	pages := (pg.Total + r.c.pageSize - 1) / r.c.pageSize
	if len(pg.Users) == 0 && page > 0 && pages > 0 {
		page = pages - 1
		if pg, res, err = r.c.listUsers(r.ctx, r.who, status, query, page); err != nil || !res.ok() {
			r.show(r.failure(res, err, "us", false))
			return
		}
	}

	var b strings.Builder
	if query != "" {
		b.WriteString(r.t("list.search_title", escapeHTML(query)))
	} else {
		b.WriteString(r.t("list.title", r.filterLabel(filter)))
	}
	b.WriteString("\n")
	if pg.Total == 0 {
		b.WriteString(r.t("list.empty"))
	} else {
		b.WriteString(r.t("list.count", pg.Total, page+1, pages))
	}

	kb := keyboard{}
	for _, u := range pg.Users {
		kb = append(kb, []button{btn(r.userRowLabel(u), "u:"+u.Username)})
	}
	if pages > 1 {
		cb := func(p int) string {
			d := fmt.Sprintf("l:%s:%d", filter, p)
			if query != "" {
				d += ":" + truncateBytes(query, searchQueryBytes)
			}
			return d
		}
		row := []button{}
		if page > 0 {
			row = append(row, btn("‹", cb(page-1)))
		}
		row = append(row, btn(fmt.Sprintf("%d/%d", page+1, pages), "nop"))
		if page+1 < pages {
			row = append(row, btn("›", cb(page+1)))
		}
		kb = append(kb, row)
	}
	kb = append(kb, r.navRow("us"))
	r.show(screen{Text: b.String(), KB: kb})
}

func (r *request) userRowLabel(u userDTO) string {
	limit := "∞"
	if u.DataLimit != nil && *u.DataLimit > 0 {
		limit = formatBytes(*u.DataLimit)
	}
	return fmt.Sprintf("%s %s · %s/%s", statusEmoji(u.Status), u.Username, formatBytes(u.UsedTraffic), limit)
}

// search handles a plain text message outside any flow: a username, or part of
// one. An exact hit opens the card straight away.
func (r *request) search(text string) {
	query := truncateBytes(text, searchQueryBytes)
	if validUsername(query) {
		// A refusal or a miss falls through to the scoped list below, so the
		// exact lookup never tells a reseller that someone else's user exists.
		if u, res, err := r.c.getUser(r.ctx, r.who, query); err == nil && res.ok() {
			r.showUser(u, "")
			return
		}
	}
	pg, res, err := r.c.listUsers(r.ctx, r.who, "", query, 0)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "h", false))
		return
	}
	if pg.Total == 1 && len(pg.Users) == 1 {
		if u, res, err := r.c.getUser(r.ctx, r.who, pg.Users[0].Username); err == nil && res.ok() {
			r.showUser(u, "")
			return
		}
	}
	r.list("*", 0, query)
}

// ---- user card ----

func (r *request) userCard(name, notice string) {
	u, res, err := r.c.getUser(r.ctx, r.who, name)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", true))
		return
	}
	r.showUser(u, notice)
}

func (r *request) showUser(u userDTO, notice string) {
	r.show(screen{Text: r.cardText(u, notice), KB: r.cardKeyboard(u)})
}

// maxCardLinks bounds how many subscription addresses one card lists, so a
// panel with many prefixes cannot push the card past Telegram's limit.
const maxCardLinks = 6

func (r *request) cardText(u userDTO, notice string) string {
	now := r.c.now()
	var b strings.Builder
	if notice != "" {
		b.WriteString("<i>" + notice + "</i>\n\n")
	}
	b.WriteString("👤 <b>" + escapeHTML(u.Username) + "</b> · " + statusEmoji(u.Status) + " " + r.statusLabel(u.Status) + "\n")

	limit := int64(0)
	if u.DataLimit != nil {
		limit = *u.DataLimit
	}
	if limit > 0 {
		b.WriteString(progressBar(u.UsedTraffic, limit, 12) + " " + fmt.Sprintf("%d%%", usagePercent(u.UsedTraffic, limit)) + "\n")
		b.WriteString(r.t("card.usage", formatBytes(u.UsedTraffic), formatBytes(limit)) + "\n")
	} else {
		b.WriteString(r.t("card.usage", formatBytes(u.UsedTraffic), r.t("card.unlimited")) + "\n")
	}

	switch {
	case u.Status == "on_hold" && u.OnHoldExpireDuration != nil && *u.OnHoldExpireDuration > 0:
		b.WriteString(r.t("card.onhold", int(math.Ceil(float64(*u.OnHoldExpireDuration)/86400))) + "\n")
	case u.Expire != nil && *u.Expire > 0:
		d := daysLeft(*u.Expire, now)
		when := time.Unix(*u.Expire, 0).UTC().Format("2006-01-02")
		if d >= 0 {
			b.WriteString(r.t("card.expire", when, r.t("card.days_left", d)) + "\n")
		} else {
			b.WriteString(r.t("card.expire", when, r.t("card.expired_ago", -d)) + "\n")
		}
	default:
		b.WriteString(r.t("card.expire_never") + "\n")
	}

	if u.OnlineAt != nil {
		b.WriteString(r.t("card.online", r.ago(*u.OnlineAt)) + "\n")
	} else {
		b.WriteString(r.t("card.online_never") + "\n")
	}
	owner := r.t("card.owner_none")
	if u.Admin != nil && u.Admin.Username != "" {
		owner = "<code>" + escapeHTML(u.Admin.Username) + "</code>"
	}
	b.WriteString(r.t("card.owner", owner) + "\n")
	if u.Note != nil && *u.Note != "" {
		b.WriteString(r.t("card.note", escapeHTML(truncateRunes(*u.Note, 300))) + "\n")
	}
	if u.SyncedFromPanelName != nil && *u.SyncedFromPanelName != "" {
		b.WriteString(r.t("card.synced", escapeHTML(*u.SyncedFromPanelName)) + "\n")
	}

	links := u.links()
	if len(links) == 0 {
		b.WriteString("\n" + r.t("card.nolinks"))
		return b.String()
	}
	b.WriteString("\n" + r.t("card.links") + "\n")
	for i, l := range links {
		if i == maxCardLinks {
			break
		}
		b.WriteString("<code>" + escapeHTML(l) + "</code>\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *request) ago(t time.Time) string {
	d := r.c.now().Sub(t)
	switch {
	case d < time.Minute:
		return r.t("ago.now")
	case d < time.Hour:
		return r.t("ago.min", int(d/time.Minute))
	case d < 48*time.Hour:
		return r.t("ago.hour", int(d/time.Hour))
	}
	return r.t("ago.day", int(d/(24*time.Hour)))
}

func (r *request) cardKeyboard(u userDTO) keyboard {
	n := u.Username
	toggle := btn(r.t("act.disable"), "t:"+n)
	if u.Status == "disabled" {
		toggle = btn(r.t("act.enable"), "t:"+n)
	}
	return keyboard{
		{toggle, btn(r.t("act.reset"), "rs:"+n)},
		{btn(r.t("act.extend"), "ex:"+n), btn(r.t("act.data"), "dt:"+n)},
		{btn(r.t("act.qr"), "qr:"+n), btn(r.t("act.note"), "nt:"+n)},
		{btn(r.t("act.revoke"), "rv:"+n), btn(r.t("act.delete"), "dl:"+n)},
		r.navRow("us"),
	}
}

// sortedKeys returns a map's keys in order, so protocol lists render stably.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
