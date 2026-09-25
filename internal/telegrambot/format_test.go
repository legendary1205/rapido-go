package telegrambot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-5, "0 B"},
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{10 * 1024 * 1024, "10.0 MB"},
		{gib, "1.00 GB"},
		{5*gib + gib/2, "5.50 GB"},
		{150 * gib, "150 GB"},
		{3 << 40, "3.00 TB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProgressBar(t *testing.T) {
	cases := []struct {
		used, limit int64
		want        string
	}{
		{0, 100, "░░░░░░░░░░"},
		{50, 100, "█████░░░░░"},
		{100, 100, "██████████"},
		{250, 100, "██████████"}, // over the limit never overflows the bar
		{1, 1000, "█░░░░░░░░░"},  // any usage shows at least one block
		{10, 0, "░░░░░░░░░░"},    // no limit: empty bar, callers print "unlimited" instead
	}
	for _, c := range cases {
		got := progressBar(c.used, c.limit, 10)
		if got != c.want {
			t.Errorf("progressBar(%d, %d) = %q, want %q", c.used, c.limit, got, c.want)
		}
		if utf8.RuneCountInString(got) != 10 {
			t.Errorf("progressBar(%d, %d) has %d cells, want 10", c.used, c.limit, utf8.RuneCountInString(got))
		}
	}
	if progressBar(1, 2, 0) != "" {
		t.Error("zero width must give an empty bar")
	}
}

func TestUsagePercent(t *testing.T) {
	if usagePercent(0, 100) != 0 || usagePercent(50, 100) != 50 || usagePercent(300, 100) != 100 {
		t.Error("usagePercent wrong on the ordinary cases")
	}
	if usagePercent(1, 1_000_000) != 1 {
		t.Error("tiny usage must not read as 0%")
	}
	if usagePercent(5, 0) != 0 {
		t.Error("no limit means 0%")
	}
}

func TestDaysLeft(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		expire int64
		want   int
	}{
		{now.Unix() + 86400*10, 10},
		{now.Unix() + 3600, 1}, // ending later today is still one day left
		{now.Unix(), 0},
		{now.Unix() - 3600, -1},
		{now.Unix() - 86400*3, -3},
	}
	for _, c := range cases {
		if got := daysLeft(c.expire, now); got != c.want {
			t.Errorf("daysLeft(now%+d) = %d, want %d", c.expire-now.Unix(), got, c.want)
		}
	}
}

func TestEscapeHTML(t *testing.T) {
	got := escapeHTML(`<b>a&b</b> "q" 'x'`)
	want := `&lt;b&gt;a&amp;b&lt;/b&gt; "q" 'x'`
	if got != want {
		t.Errorf("escapeHTML = %q, want %q", got, want)
	}
	if strings.ContainsAny(escapeHTML("<><>&<"), "<>") {
		t.Error("escapeHTML left an angle bracket")
	}
}

func TestCapMessageCutsAtLineBoundary(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	long := "<b>head</b>\n" + strings.Repeat(line, 100)
	got := capMessage(long, 1000)
	if utf8.RuneCountInString(got) > 1000 {
		t.Errorf("capMessage returned %d runes, want <= 1000", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "\n…") {
		t.Errorf("capMessage should end with a marker on its own line, got tail %q", got[len(got)-10:])
	}
	if strings.Count(got, "<b>") != strings.Count(got, "</b>") {
		t.Error("capMessage split a tag")
	}
	short := "hello"
	if capMessage(short, 1000) != short {
		t.Error("a short message must pass through unchanged")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncateRunes("سلام دنیا", 4); got != "سلام…" {
		t.Errorf("truncateRunes = %q", got)
	}
	// 3 bytes per rune: a cut inside a rune must back up, never emit a broken one.
	got := truncateBytes("日本語テキスト", 7)
	if !utf8.ValidString(got) || len(got) > 7 || got != "日本" {
		t.Errorf("truncateBytes = %q", got)
	}
	if truncateBytes("abc", 10) != "abc" {
		t.Error("truncateBytes changed a short string")
	}
}

func TestNormalizeDigits(t *testing.T) {
	if got := normalizeDigits("۱۲۳ ٤٥٦ 789 ۲٫۵"); got != "123 456 789 2.5" {
		t.Errorf("normalizeDigits = %q", got)
	}
}

func TestParseGBAndDays(t *testing.T) {
	gbCases := []struct {
		in     string
		zeroOK bool
		want   int64
		ok     bool
	}{
		{"5", false, 5 * gib, true},
		{"2.5", false, 5 * gib / 2, true},
		{"۱۰", false, 10 * gib, true},
		{"10 GB", false, 10 * gib, true},
		{"10gb", false, 10 * gib, true},
		{"0", true, 0, true},
		{"0", false, 0, false},
		{"-3", false, 0, false},
		{"abc", false, 0, false},
		{"", false, 0, false},
		{"1e3", false, 0, false},
		{"100001", false, 0, false},
	}
	for _, c := range gbCases {
		got, ok := parseGB(c.in, c.zeroOK)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseGB(%q, %v) = %d, %v; want %d, %v", c.in, c.zeroOK, got, ok, c.want, c.ok)
		}
	}
	dayCases := []struct {
		in     string
		zeroOK bool
		want   int
		ok     bool
	}{
		{"30", false, 30, true},
		{"۹۰", false, 90, true},
		{"7 days", false, 7, true},
		{"30d", false, 30, true},
		{"0", true, 0, true},
		{"0", false, 0, false},
		{"2.5", false, 0, false},
		{"3651", false, 0, false},
		{"x", false, 0, false},
	}
	for _, c := range dayCases {
		got, ok := parseDays(c.in, c.zeroOK)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseDays(%q, %v) = %d, %v; want %d, %v", c.in, c.zeroOK, got, ok, c.want, c.ok)
		}
	}
}

func TestValidUsername(t *testing.T) {
	good := []string{"abc", "alice_test", "A1_b2", "user.name", "abc-de_f", strings.Repeat("a", 32)}
	bad := []string{"", "ab", "a b", "ali/ce", "نام", "-abc", "ab-cdef", strings.Repeat("a", 33), "abc:def", strings.Repeat("a", 32) + "-xyz"}
	for _, s := range good {
		if !validUsername(s) {
			t.Errorf("validUsername(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validUsername(s) {
			t.Errorf("validUsername(%q) = true, want false", s)
		}
	}
}

func TestLangFromCode(t *testing.T) {
	if langFromCode("") != langFA || langFromCode("fa") != langFA || langFromCode("FA-IR") != langFA {
		t.Error("Persian and unknown must map to Persian")
	}
	for _, c := range []string{"en", "en-US", "ru", "zh-hans", "de"} {
		if langFromCode(c) != langEN {
			t.Errorf("langFromCode(%q) should be English", c)
		}
	}
}
