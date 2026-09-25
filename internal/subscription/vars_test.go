package subscription

import "testing"

func TestLoadVariablesRenderFromSetLoad(t *testing.T) {
	v := BuildVariables(UserInfo{Username: "alice", Status: "active"}, "203.0.113.1")
	v.SetLoad("🟢", "23%", "free")

	cases := map[string]string{
		"{LOAD}":            "🟢 23%",
		"{LOAD_EMOJI}":      "🟢",
		"{LOAD_PERCENT}":    "23%",
		"{LOAD_LEVEL}":      "free",
		"🇩🇪 Germany {LOAD}": "🇩🇪 Germany 🟢 23%",
		"{LOAD_EMOJI} {LOAD_LEVEL} ({LOAD_PERCENT}) {USERNAME}": "🟢 free (23%) alice",
	}
	for template, want := range cases {
		if got := v.FormatRemark(template); got != want {
			t.Errorf("FormatRemark(%q) = %q, want %q", template, got, want)
		}
	}
}

func TestLoadVariablesRenderEmptyWithoutDataAndTrimTheSeparator(t *testing.T) {
	v := BuildVariables(UserInfo{Username: "alice", Status: "active"}, "")
	v.SetLoad("", "", "")

	cases := map[string]string{
		"{LOAD}":                              "",
		"🇩🇪 Germany {LOAD}":                   "🇩🇪 Germany",
		"🇩🇪 Germany {LOAD}  ":                 "🇩🇪 Germany",
		"{LOAD_EMOJI} Germany":                "Germany",
		"Germany {LOAD_PERCENT} {LOAD_LEVEL}": "Germany",
		"🇩🇪 {USERNAME} {LOAD}":                "🇩🇪 alice",
	}
	for template, want := range cases {
		if got := v.FormatRemark(template); got != want {
			t.Errorf("FormatRemark(%q) = %q, want %q", template, got, want)
		}
	}
}

func TestFormatRemarkLeavesTemplatesWithoutLoadVariablesUntouched(t *testing.T) {
	v := BuildVariables(UserInfo{Username: "alice", Status: "active"}, "")
	v.SetLoad("", "", "")

	// No {LOAD...} in the template: not even trailing whitespace is trimmed,
	// and other variables render exactly as Format renders them.
	cases := map[string]string{
		"Germany  ":            "Germany  ",
		" Germany":             " Germany",
		"🛜 {DATA_LEFT} 🛜":      "🛜 ∞ 🛜",
		"{USERNAME} ":          "alice ",
		"{NOPE}":               "<missing>",
		"plain":                "plain",
		"":                     "",
		"Germany {USERNAME}  ": "Germany alice  ",
	}
	for template, want := range cases {
		if got := v.FormatRemark(template); got != want {
			t.Errorf("FormatRemark(%q) = %q, want %q", template, got, want)
		}
		if got := v.Format(template); got != want {
			t.Errorf("Format(%q) = %q, want %q", template, got, want)
		}
	}
}

func TestSetLoadOverwritesThePreviousHost(t *testing.T) {
	v := BuildVariables(UserInfo{Username: "alice", Status: "active"}, "")
	v.SetLoad("🔴", "97%", "full")
	if got := v.FormatRemark("A {LOAD}"); got != "A 🔴 97%" {
		t.Fatalf("got %q", got)
	}
	// The next host has no data: nothing of the previous one may show.
	v.SetLoad("", "", "")
	if got := v.FormatRemark("B {LOAD} {LOAD_LEVEL}"); got != "B" {
		t.Errorf("got %q, want the previous host's values gone", got)
	}
}

func TestAutoLoadSuffixOnAPlainRemark(t *testing.T) {
	v := BuildVariables(UserInfo{Username: "alice", Status: "active"}, "")

	v.SetLoad("🟡", "55%", "normal")
	if got := v.FormatRemark("🇩🇪 Germany" + AutoLoadSuffix); got != "🇩🇪 Germany 🟡 55%" {
		t.Errorf("with data: %q", got)
	}
	v.SetLoad("", "", "")
	if got := v.FormatRemark("🇩🇪 Germany" + AutoLoadSuffix); got != "🇩🇪 Germany" {
		t.Errorf("without data the suffix and its space vanish, got %q", got)
	}
}

func TestFormatFastPathMatchesTheRegexpPath(t *testing.T) {
	v := Variables{"A": "1"}
	for _, s := range []string{"", "plain", "no braces at all", "{a}", "{A}", "x{A}y{B}z"} {
		want := placeholderPattern.ReplaceAllStringFunc(s, func(token string) string {
			if val, ok := v[token[1:len(token)-1]]; ok {
				return val
			}
			return "<missing>"
		})
		if got := v.Format(s); got != want {
			t.Errorf("Format(%q) = %q, want %q", s, got, want)
		}
	}
}
