package telegrambot

import (
	"context"
	"strings"
	"time"
)

// dispatch is everything that happens to an update before any admin-facing work:
// filtering, authorisation, throttling and de-duplication. It returns quickly;
// the actual handling runs on the sender's own queue.
func (c *Console) dispatch(ctx context.Context, bot *botClient, u tgUpdate) {
	var (
		from tgUser
		msg  *tgMessage
		cb   *tgCallback
	)
	switch {
	case u.CallbackQuery != nil:
		cb = u.CallbackQuery
		from, msg = cb.From, cb.Message
		if msg == nil || msg.Chat.Type != "private" || msg.Chat.ID != from.ID {
			return
		}
	case u.Message != nil:
		msg = u.Message
		if msg.From == nil || msg.From.IsBot || msg.Chat.Type != "private" || msg.Chat.ID != msg.From.ID {
			return
		}
		from = *msg.From
		// Forwards and inline-bot results are not something the admin typed.
		if msg.Text == "" || msg.forwarded() || msg.ViaBot != nil {
			return
		}
	default:
		return
	}

	who, ok := c.authorize(ctx, from.ID)
	if !ok {
		c.refuse(ctx, bot, from, cb)
		return
	}
	if !c.allow(from.ID) {
		if cb != nil {
			_ = bot.answerCallback(ctx, cb.ID, "⏳")
		}
		return
	}
	if u.UpdateID != 0 && !c.firstDelivery(ctx, u.UpdateID) {
		return
	}

	c.enqueue(from.ID, func() {
		r := &request{c: c, ctx: ctx, bot: bot, uid: from.ID, who: who}
		r.lang = c.loadLang(ctx, from.ID, from.LanguageCode)
		if cb != nil {
			r.cbID, r.msgID = cb.ID, cb.Message.MessageID
			r.onCallback(cb.Data)
			return
		}
		r.inMsgID = msg.MessageID
		r.onText(msg.Text)
	})
}

// refuse sends the one short "not allowed" note an unknown account gets. It says
// nothing about the panel, its users or how access is granted.
func (c *Console) refuse(ctx context.Context, bot *botClient, from tgUser, cb *tgCallback) {
	if !c.shouldRefuse(from.ID) {
		return
	}
	text := tr(langFromCode(from.LanguageCode), "refuse")
	if cb != nil {
		_ = bot.answerCallback(ctx, cb.ID, truncateRunes(stripTags(text), 150))
		return
	}
	_, _ = bot.sendMessage(ctx, from.ID, text, nil)
}

func stripTags(s string) string {
	return strings.NewReplacer("<b>", "", "</b>", "", "<code>", "", "</code>", "").Replace(s)
}

// request is one admin interaction: who it is, which console message it edits.
type request struct {
	c    *Console
	ctx  context.Context
	bot  *botClient
	uid  int64
	lang lang
	who  principal

	cbID    string
	msgID   int // console message edited in place; 0 sends a new one
	inMsgID int // the admin's own text message, removed once consumed
	toast   string
	sess    *session
}

func (r *request) t(key string, args ...any) string { return tr(r.lang, key, args...) }

// screen is one rendered console view.
type screen struct {
	Text string
	KB   keyboard
}

// show edits the console message in place, or sends a fresh one when there is
// none to edit (or Telegram refuses the edit, e.g. the message is too old).
func (r *request) show(s screen) {
	text := capMessage(s.Text, maxMessageRunes)
	if r.msgID != 0 {
		err := r.bot.editMessage(r.ctx, r.uid, r.msgID, text, s.KB)
		if err == nil || isNotModified(err) {
			return
		}
	}
	id, err := r.bot.sendMessage(r.ctx, r.uid, text, s.KB)
	if err != nil {
		r.c.d.Logger.Warn("telegram console: could not send message", "error", err)
		return
	}
	r.msgID = id
}

func (r *request) answer() {
	if r.cbID == "" {
		return
	}
	_ = r.bot.answerCallback(r.ctx, r.cbID, r.toast)
	r.cbID = ""
}

// answerNow acknowledges the tap immediately, for actions that take a while.
func (r *request) answerNow(text string) {
	r.toast = text
	r.answer()
}

// deleteIncoming removes the admin's own input message so the chat stays a
// single evolving screen. Best effort: Telegram refuses for old messages.
func (r *request) deleteIncoming() {
	if r.inMsgID != 0 {
		_ = r.bot.deleteMessage(r.ctx, r.uid, r.inMsgID)
		r.inMsgID = 0
	}
}

func (r *request) saveSess(s *session) {
	s.Msg = r.msgID
	r.sess = s
	r.c.saveSession(r.ctx, r.uid, s)
}

func (r *request) clearSess() {
	r.sess = nil
	r.c.clearSession(r.ctx, r.uid)
}

func (r *request) onCallback(data string) {
	defer r.answer()
	op, rest, _ := strings.Cut(data, ":")
	if op != "nu" && op != "nop" {
		r.clearSess()
	}
	switch op {
	case "nop":
	case "h":
		r.home()
	case "hp":
		r.help()
	case "lg":
		r.toggleLang()
	case "sy":
		r.system()
	case "nd":
		r.nodes()
	case "us":
		r.usersMenu()
	case "l":
		r.listFromCallback(rest)
	case "u":
		r.userCard(rest, "")
	case "t":
		r.toggleUser(rest)
	case "rs":
		r.resetUsage(strings.Split(rest, ":"))
	case "rv":
		r.revokeSub(strings.Split(rest, ":"))
	case "dl":
		r.deleteUser(strings.Split(rest, ":"))
	case "ex":
		r.extend(strings.Split(rest, ":"))
	case "dt":
		r.addData(strings.Split(rest, ":"))
	case "nt":
		r.askNote(rest)
	case "qr":
		r.qr(rest)
	case "nu":
		r.wizard(strings.Split(rest, ":"))
	case "bk":
		r.backup(rest == "y")
	default:
		r.home()
	}
}

func (r *request) onText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		r.onCommand(text)
		return
	}
	if utf8Len(text) > maxInputRunes {
		r.show(r.errorScreen(r.t("in.toolong"), "h"))
		return
	}
	if s, ok := r.c.loadSession(r.ctx, r.uid); ok {
		r.sess = s
		r.msgID = s.Msg
		r.input(s, text)
		return
	}
	r.search(text)
}

func (r *request) onCommand(text string) {
	cmd := strings.ToLower(strings.SplitN(strings.Fields(text)[0], "@", 2)[0])
	r.clearSess()
	switch cmd {
	case "/help":
		r.help()
	default: // /start, /menu, /cancel and anything unknown
		r.home()
	}
}

// input feeds a plain text message to whatever the session is waiting for.
func (r *request) input(s *session, text string) {
	defer r.deleteIncoming()
	switch s.Mode {
	case modeExtend:
		r.customExtend(s, text)
	case modeData:
		r.customData(s, text)
	case modeNote:
		r.setNote(s, text)
	case modeWizard:
		r.wizardInput(s, text)
	default:
		r.clearSess()
		r.search(text)
	}
}

// maxInputRunes is generous enough for a 500-byte note; anything longer is
// certainly not something the console can use.
const maxInputRunes = 512

func utf8Len(s string) int { return len([]rune(s)) }

// wrapCtx gives one API call its own deadline.
func (r *request) wrapCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.ctx, d)
}

const apiTimeout = 30 * time.Second

func (r *request) call(method, path string, body any) (apiResult, error) {
	ctx, cancel := r.wrapCtx(apiTimeout)
	defer cancel()
	return r.c.call(ctx, r.who, method, path, body)
}
