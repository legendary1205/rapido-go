package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- simple user actions ----

func (r *request) confirmScreen(key, name, yesData string) screen {
	return screen{
		Text: r.t(key, "<b>"+escapeHTML(name)+"</b>"),
		KB: keyboard{
			{btn(r.t("btn.yes"), yesData), btn(r.t("btn.cancel"), "u:"+name)},
			{btn(r.t("btn.home"), "h")},
		},
	}
}

func isConfirmed(parts []string) bool { return len(parts) > 1 && parts[1] == "y" }

func (r *request) toggleUser(name string) {
	u, res, err := r.c.getUser(r.ctx, r.who, name)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", true))
		return
	}
	next, notice := "disabled", r.t("done.disabled")
	if u.Status == "disabled" {
		next, notice = "active", r.t("done.enabled")
	}
	updated, res, err := r.c.modifyUser(r.ctx, r.who, u, map[string]any{"status": next})
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "u:"+name, true))
		return
	}
	r.toast = notice
	r.showUser(updated, notice)
}

// postUser runs a user route that answers with the updated user (reset, revoke).
func (r *request) postUser(name, action, notice string) {
	res, err := r.call(http.MethodPost, userPath(name)+"/"+action, nil)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "u:"+name, true))
		return
	}
	var u userDTO
	if err := res.decode(&u); err != nil {
		r.show(r.failure(res, err, "u:"+name, true))
		return
	}
	r.toast = notice
	r.showUser(u, notice)
}

func (r *request) resetUsage(parts []string) {
	name := parts[0]
	if !isConfirmed(parts) {
		r.show(r.confirmScreen("confirm.reset", name, "rs:"+name+":y"))
		return
	}
	r.postUser(name, "reset", r.t("done.reset"))
}

func (r *request) revokeSub(parts []string) {
	name := parts[0]
	if !isConfirmed(parts) {
		r.show(r.confirmScreen("confirm.revoke", name, "rv:"+name+":y"))
		return
	}
	r.postUser(name, "revoke_sub", r.t("done.revoked"))
}

func (r *request) deleteUser(parts []string) {
	name := parts[0]
	if !isConfirmed(parts) {
		r.show(r.confirmScreen("confirm.delete", name, "dl:"+name+":y"))
		return
	}
	res, err := r.call(http.MethodDelete, userPath(name), nil)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", true))
		return
	}
	r.toast = r.t("done.deleted")
	r.show(screen{
		Text: r.t("done.deleted_user", "<b>"+escapeHTML(name)+"</b>"),
		KB:   keyboard{r.navRow("us")},
	})
}

// ---- extend expiry / add data ----

func splitArg(parts []string) (name, arg string) {
	name = parts[0]
	if len(parts) > 1 {
		arg = parts[1]
	}
	return name, arg
}

func (r *request) extend(parts []string) {
	name, arg := splitArg(parts)
	switch arg {
	case "":
		r.show(screen{
			Text: r.t("ext.title", "<b>"+escapeHTML(name)+"</b>"),
			KB: keyboard{
				{btn("+7", "ex:"+name+":7"), btn("+30", "ex:"+name+":30"), btn("+90", "ex:"+name+":90")},
				{btn(r.t("btn.custom"), "ex:"+name+":c")},
				r.navRow("u:" + name),
			},
		})
	case "c":
		r.saveSess(&session{Mode: modeExtend, User: name})
		r.show(r.promptScreen(r.t("ext.prompt"), "ex:"+name))
	default:
		days, ok := parseDays(arg, false)
		if !ok {
			r.userCard(name, "")
			return
		}
		r.applyExtend(name, days)
	}
}

func (r *request) promptScreen(text, back string) screen {
	return screen{Text: text, KB: keyboard{r.navRow(back)}}
}

func (r *request) customExtend(s *session, text string) {
	days, ok := parseDays(text, false)
	if !ok {
		r.saveSess(s)
		r.show(r.promptScreen("❌ "+r.t("in.num_invalid")+"\n\n"+r.t("ext.prompt"), "ex:"+s.User))
		return
	}
	r.clearSess()
	r.applyExtend(s.User, days)
}

func (r *request) applyExtend(name string, days int) {
	u, res, err := r.c.getUser(r.ctx, r.who, name)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", true))
		return
	}
	add := int64(days) * 86400
	fields := map[string]any{}
	switch {
	case u.Status == "on_hold" && u.OnHoldExpireDuration != nil && *u.OnHoldExpireDuration > 0:
		fields["on_hold_expire_duration"] = *u.OnHoldExpireDuration + add
	case u.Expire != nil && *u.Expire > 0:
		// Renewing an expired user counts from today, not from a date long past.
		base := max(*u.Expire, r.c.now().Unix())
		fields["expire"] = base + add
	default:
		r.showUser(u, r.t("ext.unlimited"))
		return
	}
	updated, res, err := r.c.modifyUser(r.ctx, r.who, u, fields)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "u:"+name, true))
		return
	}
	notice := r.t("done.extended", days)
	r.toast = notice
	r.showUser(updated, notice)
}

func (r *request) addData(parts []string) {
	name, arg := splitArg(parts)
	switch arg {
	case "":
		r.show(screen{
			Text: r.t("dat.title", "<b>"+escapeHTML(name)+"</b>"),
			KB: keyboard{
				{btn("+5 GB", "dt:"+name+":5"), btn("+10 GB", "dt:"+name+":10"), btn("+50 GB", "dt:"+name+":50")},
				{btn(r.t("btn.custom"), "dt:"+name+":c")},
				r.navRow("u:" + name),
			},
		})
	case "c":
		r.saveSess(&session{Mode: modeData, User: name})
		r.show(r.promptScreen(r.t("dat.prompt"), "dt:"+name))
	default:
		add, ok := parseGB(arg, false)
		if !ok {
			r.userCard(name, "")
			return
		}
		r.applyData(name, add)
	}
}

func (r *request) customData(s *session, text string) {
	add, ok := parseGB(text, false)
	if !ok {
		r.saveSess(s)
		r.show(r.promptScreen("❌ "+r.t("in.num_invalid")+"\n\n"+r.t("dat.prompt"), "dt:"+s.User))
		return
	}
	r.clearSess()
	r.applyData(s.User, add)
}

func (r *request) applyData(name string, add int64) {
	u, res, err := r.c.getUser(r.ctx, r.who, name)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", true))
		return
	}
	if u.DataLimit == nil || *u.DataLimit <= 0 {
		r.showUser(u, r.t("dat.unlimited"))
		return
	}
	updated, res, err := r.c.modifyUser(r.ctx, r.who, u, map[string]any{"data_limit": *u.DataLimit + add})
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "u:"+name, true))
		return
	}
	notice := r.t("done.data", formatBytes(add))
	r.toast = notice
	r.showUser(updated, notice)
}

// ---- note ----

// maxNoteBytes matches the panel, which counts bytes, not characters.
const maxNoteBytes = 500

func (r *request) askNote(name string) {
	r.saveSess(&session{Mode: modeNote, User: name})
	r.show(r.promptScreen(r.t("note.prompt"), "u:"+name))
}

func (r *request) setNote(s *session, text string) {
	if len(text) > maxNoteBytes {
		r.saveSess(s)
		r.show(r.promptScreen("❌ "+r.t("note.toolong")+"\n\n"+r.t("note.prompt"), "u:"+s.User))
		return
	}
	note := text
	if note == "-" {
		note = ""
	}
	u, res, err := r.c.getUser(r.ctx, r.who, s.User)
	if err != nil || !res.ok() {
		r.clearSess()
		r.show(r.failure(res, err, "us", true))
		return
	}
	updated, res, err := r.c.modifyUser(r.ctx, r.who, u, map[string]any{"note": note})
	r.clearSess()
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "u:"+s.User, true))
		return
	}
	r.showUser(updated, r.t("done.note"))
}

// ---- QR ----

func (r *request) qr(name string) {
	u, res, err := r.c.getUser(r.ctx, r.who, name)
	if err != nil || !res.ok() {
		r.show(r.failure(res, err, "us", true))
		return
	}
	if r.sendQR(u) {
		r.toast = r.t("done.qr")
	} else {
		r.toast = r.t("card.nolinks_short")
	}
}

// sendQR sends the QR code of the first subscription address as a photo. A user
// with no address, or a failed render, simply gets no image: the link is already
// in the card as text.
func (r *request) sendQR(u userDTO) bool {
	links := u.links()
	if len(links) == 0 {
		return false
	}
	png, err := qrPNG(links[0])
	if err != nil {
		r.c.d.Logger.Warn("telegram console: could not render QR", "error", err)
		return false
	}
	if err := r.bot.sendPhoto(r.ctx, r.uid, png, "<b>"+escapeHTML(u.Username)+"</b>"); err != nil {
		r.c.d.Logger.Warn("telegram console: could not send QR", "error", err)
		return false
	}
	return true
}

// ---- new user wizard ----

func finalUsername(d draft) string { return d.Prefix + d.Name + d.Suffix }

func (r *request) wizard(parts []string) {
	sub, arg := "", ""
	if len(parts) > 0 {
		sub = parts[0]
	}
	if len(parts) > 1 {
		arg = parts[1]
	}
	if sub == "" {
		s := &session{Mode: modeWizard, Step: stepName}
		r.saveSess(s)
		r.show(r.wizardScreen(s, ""))
		return
	}
	s, ok := r.c.loadSession(r.ctx, r.uid)
	if !ok || s.Mode != modeWizard {
		r.show(r.errorScreen(r.t("nu.expired"), "h"))
		return
	}
	r.sess = s

	switch sub {
	case "t":
		r.wizardTemplate(s, arg)
		return
	case "l":
		limit, ok := parseGB(arg, true)
		if !ok {
			break
		}
		s.Draft.LimitBytes = limit
		s.Step = stepDays
	case "d":
		days, ok := parseDays(arg, true)
		if !ok {
			break
		}
		s.Draft.ExpireSecs = int64(days) * 86400
		s.Step = stepConfirm
	case "n":
		s.Step = stepName
	case "b":
		s.Step = previousStep(s)
		if s.Step == "" {
			r.clearSess()
			r.home()
			return
		}
	case "ok":
		r.wizardCreate(s)
		return
	}
	r.saveSess(s)
	r.show(r.wizardScreen(s, ""))
}

func previousStep(s *session) string {
	switch s.Step {
	case stepTmpl:
		return stepName
	case stepLimit:
		if s.Draft.HasTmpl {
			return stepTmpl
		}
		return stepName
	case stepDays:
		return stepLimit
	case stepConfirm:
		if s.Draft.TmplID != 0 {
			return stepTmpl
		}
		return stepDays
	}
	return ""
}

func (r *request) wizardTemplate(s *session, arg string) {
	id, err := strconv.Atoi(arg)
	if err != nil || id < 0 {
		r.show(r.wizardScreen(s, ""))
		return
	}
	if id == 0 {
		s.Draft = draft{Name: s.Draft.Name, HasTmpl: s.Draft.HasTmpl}
		s.Step = stepLimit
		r.saveSess(s)
		r.show(r.wizardScreen(s, ""))
		return
	}
	res, cerr := r.call(http.MethodGet, "/api/user_template/"+strconv.Itoa(id), nil)
	var t templateDTO
	if cerr != nil || !res.ok() || res.decode(&t) != nil {
		r.show(r.failure(res, cerr, "nu:b", false))
		return
	}
	s.Draft.TmplID, s.Draft.TmplName = t.ID, t.Name
	s.Draft.LimitBytes, s.Draft.ExpireSecs = t.DataLimit, t.ExpireDuration
	s.Draft.Prefix, s.Draft.Suffix = "", ""
	if t.UsernamePrefix != nil {
		s.Draft.Prefix = *t.UsernamePrefix
	}
	if t.UsernameSuffix != nil {
		s.Draft.Suffix = *t.UsernameSuffix
	}
	s.Draft.Inbounds = t.Inbounds
	s.Step = stepConfirm
	r.saveSess(s)
	r.show(r.wizardScreen(s, ""))
}

// wizardInput handles typed text at a wizard step.
func (r *request) wizardInput(s *session, text string) {
	switch s.Step {
	case stepName:
		if !validUsername(text) {
			r.saveSess(s)
			r.show(r.wizardScreen(s, "❌ "+r.t("nu.name_invalid")))
			return
		}
		s.Draft.Name = text
		templates, res, err := r.templates()
		if err != nil || !res.ok() {
			r.show(r.failure(res, err, "h", false))
			return
		}
		s.Draft.HasTmpl = len(templates) > 0
		s.Step = stepLimit
		if s.Draft.HasTmpl {
			s.Step = stepTmpl
		}
	case stepLimit:
		limit, ok := parseGB(text, true)
		if !ok {
			r.saveSess(s)
			r.show(r.wizardScreen(s, "❌ "+r.t("in.num_invalid")))
			return
		}
		s.Draft.LimitBytes = limit
		s.Step = stepDays
	case stepDays:
		days, ok := parseDays(text, true)
		if !ok {
			r.saveSess(s)
			r.show(r.wizardScreen(s, "❌ "+r.t("in.num_invalid")))
			return
		}
		s.Draft.ExpireSecs = int64(days) * 86400
		s.Step = stepConfirm
	default:
		r.saveSess(s)
		r.show(r.wizardScreen(s, r.t("nu.usebuttons")))
		return
	}
	r.saveSess(s)
	r.show(r.wizardScreen(s, ""))
}

func (r *request) templates() ([]templateDTO, apiResult, error) {
	res, err := r.call(http.MethodGet, "/api/user_template", nil)
	if err != nil || !res.ok() {
		return nil, res, err
	}
	var list []templateDTO
	return list, res, res.decode(&list)
}

func (r *request) wizardScreen(s *session, notice string) screen {
	prefix := ""
	if notice != "" {
		prefix = notice + "\n\n"
	}
	d := s.Draft
	switch s.Step {
	case stepTmpl:
		templates, res, err := r.templates()
		if err != nil || !res.ok() {
			return r.failure(res, err, "h", false)
		}
		kb := keyboard{}
		for i, t := range templates {
			if i == 20 {
				break
			}
			kb = append(kb, []button{btn(r.templateLabel(t), "nu:t:"+strconv.Itoa(int(t.ID)))})
		}
		kb = append(kb, []button{btn(r.t("nu.no_tmpl"), "nu:t:0")})
		kb = append(kb, r.navRow("nu:b"))
		return screen{Text: prefix + r.t("nu.tmpl", "<code>"+escapeHTML(d.Name)+"</code>"), KB: kb}
	case stepLimit:
		return screen{Text: prefix + r.t("nu.limit", "<code>"+escapeHTML(d.Name)+"</code>"), KB: keyboard{
			{btn("∞", "nu:l:0"), btn("5", "nu:l:5"), btn("10", "nu:l:10")},
			{btn("20", "nu:l:20"), btn("50", "nu:l:50"), btn("100", "nu:l:100")},
			r.navRow("nu:b"),
		}}
	case stepDays:
		return screen{Text: prefix + r.t("nu.days", "<code>"+escapeHTML(d.Name)+"</code>"), KB: keyboard{
			{btn("∞", "nu:d:0"), btn("7", "nu:d:7"), btn("30", "nu:d:30")},
			{btn("60", "nu:d:60"), btn("90", "nu:d:90"), btn("180", "nu:d:180")},
			{btn("365", "nu:d:365")},
			r.navRow("nu:b"),
		}}
	case stepConfirm:
		return screen{Text: prefix + r.wizardSummary(d), KB: keyboard{
			{btn(r.t("nu.create"), "nu:ok")},
			r.navRow("nu:b"),
		}}
	}
	return screen{Text: prefix + r.t("nu.name"), KB: keyboard{r.navRow("")}}
}

func (r *request) templateLabel(t templateDTO) string {
	limit := "∞"
	if t.DataLimit > 0 {
		limit = formatBytes(t.DataLimit)
	}
	days := "∞"
	if t.ExpireDuration > 0 {
		days = r.t("nu.days_short", (t.ExpireDuration+86399)/86400)
	}
	return fmt.Sprintf("%s · %s · %s", truncateRunes(t.Name, 24), limit, days)
}

func (r *request) wizardSummary(d draft) string {
	limit := r.t("card.unlimited")
	if d.LimitBytes > 0 {
		limit = formatBytes(d.LimitBytes)
	}
	life := r.t("card.unlimited")
	if d.ExpireSecs > 0 {
		life = r.t("nu.days_short", (d.ExpireSecs+86399)/86400)
	}
	var b strings.Builder
	b.WriteString(r.t("nu.confirm") + "\n")
	b.WriteString("👤 <code>" + escapeHTML(finalUsername(d)) + "</code>\n")
	if d.TmplID != 0 {
		b.WriteString(r.t("nu.summary_tmpl", escapeHTML(truncateRunes(d.TmplName, 40))) + "\n")
	}
	b.WriteString(r.t("nu.summary_limit", limit) + "\n")
	b.WriteString(r.t("nu.summary_life", life))
	if protocols := sortedKeys(d.Inbounds); len(protocols) > 0 {
		b.WriteString("\n" + r.t("nu.summary_protocols", escapeHTML(strings.Join(protocols, ", "))))
	}
	return b.String()
}

var errNoInbounds = errors.New("no inbounds")

// creationProxies picks the protocols a new user gets: the template's, or every
// protocol that has at least one inbound. Each proxy is sent empty so the panel
// generates its own secrets.
func (r *request) creationProxies(d draft) (map[string]any, error) {
	protocols := sortedKeys(d.Inbounds)
	if len(protocols) == 0 {
		res, err := r.call(http.MethodGet, "/api/inbounds", nil)
		if err != nil {
			return nil, err
		}
		if !res.ok() {
			return nil, fmt.Errorf("inbounds: HTTP %d", res.Status)
		}
		var byProtocol map[string][]json.RawMessage
		if err := res.decode(&byProtocol); err != nil {
			return nil, err
		}
		for p, list := range byProtocol {
			if len(list) > 0 {
				protocols = append(protocols, p)
			}
		}
		if len(protocols) == 0 {
			return nil, errNoInbounds
		}
	}
	flow := ""
	if vals, err := r.c.d.Settings(r.ctx); err == nil {
		flow = vals.TelegramDefaultVlessFlow
	}
	proxies := make(map[string]any, len(protocols))
	for _, p := range protocols {
		if p == "vless" && flow != "" {
			proxies[p] = map[string]any{"flow": flow}
		} else {
			proxies[p] = map[string]any{}
		}
	}
	return proxies, nil
}

func (r *request) wizardCreate(s *session) {
	d := s.Draft
	name := finalUsername(d)
	if !validUsername(name) {
		s.Step = stepName
		r.saveSess(s)
		r.show(r.wizardScreen(s, "❌ "+r.t("nu.name_invalid")))
		return
	}
	proxies, err := r.creationProxies(d)
	if errors.Is(err, errNoInbounds) {
		r.show(r.wizardFailure(r.errorScreen(r.t("nu.no_inbounds"), "nu:b")))
		return
	}
	if err != nil {
		r.show(r.wizardFailure(r.failure(apiResult{}, err, "nu:b", false)))
		return
	}

	payload := map[string]any{"username": name, "proxies": proxies}
	if d.LimitBytes > 0 {
		payload["data_limit"] = d.LimitBytes
	}
	if d.ExpireSecs > 0 {
		payload["expire"] = r.c.now().Unix() + d.ExpireSecs
	}
	if len(d.Inbounds) > 0 {
		payload["inbounds"] = d.Inbounds
	}
	res, err := r.call(http.MethodPost, "/api/user", payload)
	if err != nil || !res.ok() {
		r.show(r.wizardFailure(r.failure(res, err, "nu:b", false)))
		return
	}
	var u userDTO
	if err := res.decode(&u); err != nil {
		r.show(r.wizardFailure(r.failure(res, err, "nu:b", false)))
		return
	}
	r.clearSess()
	r.toast = r.t("nu.created")
	r.showUser(u, r.t("nu.created"))
	r.sendQR(u)
}

// wizardFailure adds a way to pick another name to a wizard error screen; the
// session is kept so nothing already entered is lost.
func (r *request) wizardFailure(s screen) screen {
	kb := keyboard{{btn(r.t("nu.change_name"), "nu:n")}}
	return screen{Text: s.Text, KB: append(kb, s.KB...)}
}

// ---- backup ----

// maxBackupBytes is Telegram's upload ceiling for bots, with a little margin.
const maxBackupBytes = 49 << 20

func (r *request) backup(confirmed bool) {
	if !r.who.IsSudo {
		r.show(r.errorScreen(r.t("err.forbidden"), ""))
		return
	}
	if !confirmed {
		r.show(screen{Text: r.t("bk.confirm"), KB: keyboard{
			{btn(r.t("bk.create"), "bk:y")},
			r.navRow(""),
		}})
		return
	}
	// The dump can take a while; release the tap now and keep the screen honest.
	r.answerNow(r.t("bk.working_short"))
	r.show(screen{Text: r.t("bk.working")})

	ctx, cancel := context.WithTimeout(r.ctx, 6*time.Minute)
	defer cancel()
	res, err := r.c.call(ctx, r.who, http.MethodPost, "/api/settings/backup", nil)
	var info backupInfo
	if err != nil || !res.ok() || res.decode(&info) != nil || info.Filename == "" {
		r.show(r.backupFailure(res, err))
		return
	}
	if info.SizeBytes > maxBackupBytes {
		r.show(screen{Text: "⚠️ " + r.t("bk.toolarge", formatBytes(info.SizeBytes)), KB: keyboard{r.navRow("")}})
		return
	}
	dl, err := r.c.call(ctx, r.who, http.MethodGet, "/api/settings/backup/"+info.Filename, nil)
	if err != nil || !dl.ok() {
		r.show(r.backupFailure(dl, err))
		return
	}
	if len(dl.Body) > maxBackupBytes {
		r.show(screen{Text: "⚠️ " + r.t("bk.toolarge", formatBytes(int64(len(dl.Body)))), KB: keyboard{r.navRow("")}})
		return
	}
	caption := "💾 <code>" + escapeHTML(info.Filename) + "</code> · " + formatBytes(int64(len(dl.Body)))
	if err := r.bot.sendDocument(ctx, r.uid, info.Filename, dl.Body, caption); err != nil {
		r.c.d.Logger.Warn("telegram console: could not send backup", "error", err)
		r.show(r.errorScreen(r.t("bk.sendfail"), ""))
		return
	}
	r.show(screen{Text: "✅ " + r.t("bk.done"), KB: keyboard{r.navRow("")}})
}

func (r *request) backupFailure(res apiResult, err error) screen {
	if err == nil && res.Status != http.StatusForbidden && res.Status != http.StatusUnauthorized {
		if d := res.detail(); d != "" {
			return r.errorScreen(r.t("bk.fail", escapeHTML(d)), "")
		}
	}
	return r.failure(res, err, "", false)
}
