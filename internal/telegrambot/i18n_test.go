package telegrambot

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var verbRe = regexp.MustCompile(`%[a-zA-Z]`)

func TestCatalogsHaveTheSameKeysAndVerbs(t *testing.T) {
	fa, en := catalogs[langFA], catalogs[langEN]
	for k, v := range en {
		other, ok := fa[k]
		if !ok {
			t.Errorf("key %q is missing in Persian", k)
			continue
		}
		if a, b := len(verbRe.FindAllString(v, -1)), len(verbRe.FindAllString(other, -1)); a != b {
			t.Errorf("key %q takes %d args in English but %d in Persian", k, a, b)
		}
	}
	for k := range fa {
		if _, ok := en[k]; !ok {
			t.Errorf("key %q is missing in English", k)
		}
	}
}

// TestEveryKeyInSourceExists finds catalog keys by shape in the non-test source
// so a typo becomes a failing test, not a screen that prints "btn.hom".
func TestEveryKeyInSourceExists(t *testing.T) {
	keyRe := regexp.MustCompile(`"((?:btn|act|ago|bk|card|confirm|dat|done|err|ext|flt|help|home|in|list|nodes|note|nu|role|sys|users|wg)\.[a-z_0-9]+|refuse)"`)
	files, _ := filepath.Glob("*.go")
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "i18n.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(src), -1) {
			seen++
			if !hasKey(m[1]) {
				t.Errorf("%s uses catalog key %q that does not exist", f, m[1])
			}
		}
	}
	if seen < 50 {
		t.Errorf("only found %d keys in the source; the scan regexp is probably stale", seen)
	}
	for _, s := range []string{"active", "disabled", "limited", "expired", "on_hold"} {
		if !hasKey("st." + s) {
			t.Errorf("missing status label st.%s", s)
		}
	}
	for _, s := range []string{"connected", "connecting", "error", "disabled"} {
		if !hasKey("ns." + s) {
			t.Errorf("missing node status label ns.%s", s)
		}
	}
}

func TestTrFallsBackToEnglishThenKey(t *testing.T) {
	if got := tr(langFA, "btn.home"); got == "btn.home" || got == tr(langEN, "btn.home") {
		t.Errorf("Persian home button = %q", got)
	}
	if got := tr(langEN, "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key should render as itself, got %q", got)
	}
}
