package telegrambot

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func viewRequest(l lang) *request {
	c := New(Deps{})
	c.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return &request{c: c, lang: l}
}

func ptr[T any](v T) *T { return &v }

func TestCardTextForCommonShapes(t *testing.T) {
	r := viewRequest(langEN)
	now := r.c.now()

	unlimited := r.cardText(userDTO{Username: "free_u", Status: "active", UsedTraffic: 3 * gib}, "")
	mustContain(t, unlimited, "<b>free_u</b>")
	mustContain(t, unlimited, "3.00 GB / unlimited")
	mustContain(t, unlimited, "Expires: never")
	mustContain(t, unlimited, "Last seen: never")
	mustNotContain(t, unlimited, "█") // no limit, so no progress bar

	expired := r.cardText(userDTO{
		Username: "old_u", Status: "expired", DataLimit: ptr(10 * gib), UsedTraffic: 10 * gib,
		Expire: ptr(now.Unix() - 3*86400),
	}, "")
	mustContain(t, expired, "Expired")
	mustContain(t, expired, "expired 3 days ago")
	mustContain(t, expired, "100%")
	mustContain(t, expired, strings.Repeat("█", 12))

	hold := r.cardText(userDTO{Username: "wait_u", Status: "on_hold", OnHoldExpireDuration: ptr(int64(14 * 86400))}, "")
	mustContain(t, hold, "On hold")
	mustContain(t, hold, "14 days once connected")

	seen := now.Add(-2 * time.Hour)
	online := r.cardText(userDTO{Username: "on_u", Status: "active", OnlineAt: &seen, Admin: &adminRef{Username: "reseller1"}}, "Done")
	mustContain(t, online, "<i>Done</i>")
	mustContain(t, online, "2 h ago")
	mustContain(t, online, "<code>reseller1</code>")

	noLinks := r.cardText(userDTO{Username: "nolink_u", Status: "active"}, "")
	mustContain(t, noLinks, "No subscription link")

	// The same card in Persian carries the Persian labels.
	fa := viewRequest(langFA).cardText(userDTO{Username: "free_u", Status: "active"}, "")
	mustContain(t, fa, "نامحدود")
	mustNotContain(t, fa, "unlimited")
}

func TestCardTextEscapesEverythingAnAdminCanType(t *testing.T) {
	r := viewRequest(langEN)
	u := userDTO{
		Username: "x_user", Status: "active",
		Note:                ptr(`</code><a href="https://evil.example">click</a> & <b>`),
		Admin:               &adminRef{Username: `a<b>d`},
		SyncedFromPanelName: ptr("<i>peer</i>"),
		SubscriptionURLs:    []string{`https://p.test/sub/a&b<c>`},
	}
	card := r.cardText(u, "")
	for _, raw := range []string{"<a href", "<i>peer", "a<b>d", "a&b<c>", "</code><a"} {
		mustNotContain(t, card, raw)
	}
	mustContain(t, card, "&lt;a href=")
	mustContain(t, card, "a&amp;b&lt;c&gt;")
	if strings.Count(card, "<code>") != strings.Count(card, "</code>") || strings.Count(card, "<b>") != strings.Count(card, "</b>") {
		t.Errorf("unbalanced markup in card:\n%s", card)
	}
}

func TestCardStaysUnderTheMessageLimitWithManyLongLinks(t *testing.T) {
	r := viewRequest(langEN)
	var links []string
	for i := 0; i < 20; i++ {
		links = append(links, fmt.Sprintf("https://sub%02d.example.com/sub/%s", i, strings.Repeat("t", 240)))
	}
	u := userDTO{Username: "big_u", Status: "active", Note: ptr(strings.Repeat("ی", 900)), SubscriptionURLs: links}
	card := capMessage(r.cardText(u, "notice"), maxMessageRunes)
	if n := utf8.RuneCountInString(card); n > 4096 {
		t.Errorf("card is %d characters, Telegram's limit is 4096", n)
	}
	if strings.Count(card, "<code>https://") != maxCardLinks {
		t.Errorf("card lists %d links, want the cap of %d", strings.Count(card, "<code>https://"), maxCardLinks)
	}
	if !strings.Contains(card, "sub00.example.com") {
		t.Error("the first (primary) link must be the one kept")
	}
}

func TestCardKeyboardOffersEnableOnlyForDisabledUsers(t *testing.T) {
	r := viewRequest(langEN)
	label := func(u userDTO, data string) string {
		for _, row := range r.cardKeyboard(u) {
			for _, b := range row {
				if b.Data == data {
					return b.Text
				}
			}
		}
		return ""
	}
	if got := label(userDTO{Username: "a_user", Status: "active"}, "t:a_user"); !strings.Contains(got, "Disable") {
		t.Errorf("active user's toggle = %q", got)
	}
	if got := label(userDTO{Username: "a_user", Status: "disabled"}, "t:a_user"); !strings.Contains(got, "Enable") {
		t.Errorf("disabled user's toggle = %q", got)
	}
}

func TestEveryKeyboardOfTheCardEndsWithBackAndHome(t *testing.T) {
	r := viewRequest(langEN)
	kb := r.cardKeyboard(userDTO{Username: strings.Repeat("a", 32) + "-x", Status: "active"})
	last := kb[len(kb)-1]
	if len(last) != 2 || last[1].Data != "h" {
		t.Errorf("last row = %+v, want Back and Home", last)
	}
	for _, row := range kb {
		for _, b := range row {
			if len(b.Data) > 64 {
				t.Errorf("callback data %q is %d bytes", b.Data, len(b.Data))
			}
		}
	}
}

func TestTunnelLineNamesTheTunnelThatIsDown(t *testing.T) {
	r := viewRequest(langEN)
	if r.tunnelLine(nil) != "" {
		t.Error("no tunnels means no line")
	}
	age := 12.0
	line := r.tunnelLine([]tunnelDTO{
		{Name: "wg-fra", Up: true, Present: true, HandshakeAgeSeconds: &age},
		{Name: "wg-ams", Up: false, Present: true},
		{Name: "wg-gone", Up: true, Present: false},
		{Name: "<x>", Up: true, Present: true},
	})
	mustContain(t, line, "✅ wg-fra (12s)")
	mustContain(t, line, "❌ wg-ams (down)")
	mustContain(t, line, "❌ wg-gone (missing)")
	mustContain(t, line, "&lt;x&gt;")
	mustNotContain(t, line, "<x>")
}

func TestShortAge(t *testing.T) {
	cases := map[float64]string{-5: "0s", 12: "12s", 89: "89s", 120: "2m", 5000: "83m", 7200: "2h", 200000: "2d"}
	for in, want := range cases {
		if got := shortAge(in); got != want {
			t.Errorf("shortAge(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestUserRowLabelFitsAButton(t *testing.T) {
	r := viewRequest(langEN)
	got := r.userRowLabel(userDTO{Username: "alice_a", Status: "limited", UsedTraffic: 10 * gib, DataLimit: ptr(10 * gib)})
	if got != "🟠 alice_a · 10.0 GB/10.0 GB" {
		t.Errorf("row label = %q", got)
	}
	if got := r.userRowLabel(userDTO{Username: "free_u", Status: "active"}); !strings.HasSuffix(got, "0 B/∞") {
		t.Errorf("unlimited row label = %q", got)
	}
}

func TestFailureNeverDistinguishesMissingFromForbiddenForUsers(t *testing.T) {
	r := viewRequest(langEN)
	notFound := r.failure(apiResult{Status: 404, Body: []byte(`{"detail":"User not found"}`)}, nil, "us", true)
	forbidden := r.failure(apiResult{Status: 403, Body: []byte(`{"detail":"You're not allowed"}`)}, nil, "us", true)
	if notFound.Text != forbidden.Text {
		t.Errorf("a missing user (%q) and someone else's user (%q) read differently", notFound.Text, forbidden.Text)
	}
	other := r.failure(apiResult{Status: 422, Body: []byte(`{"detail":"Username <bad> & worse"}`)}, nil, "h", false)
	mustContain(t, other.Text, "Username &lt;bad&gt; &amp; worse") // the panel's reason, escaped
	if r.failure(apiResult{Status: 500}, nil, "h", false).Text == "" {
		t.Error("an empty API failure must still say something")
	}
}
