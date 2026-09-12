package subscription

import (
	"encoding/json"
	"os"

	"github.com/goccy/go-yaml"
)

// LoadYAMLTemplate reads and parses a YAML file into a plain map, or
// returns ok=false if path is empty or the file can't be read/parsed.
// Mirrors the current Python system's own template override mechanism
// (config.py's *_SUBSCRIPTION_TEMPLATE env vars pointing at a file under
// TEMPLATES_DIRECTORY, falling back to a packaged default when the
// operator hasn't dropped a custom one in place) - callers fall back to
// a built-in minimal default in either failure case, same as
// render_template's own TemplateNotFound handling.
func LoadYAMLTemplate(path string) (map[string]any, bool) {
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	return doc, true
}

// LoadJSONTemplate is LoadYAMLTemplate's JSON equivalent, for the v2ray
// and sing-box subscription templates (both plain JSON on the Python
// side too).
func LoadJSONTemplate(path string) (map[string]any, bool) {
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	return doc, true
}

// shallowCopyMap is used everywhere a loaded template gets per-request
// fields (proxies, remarks, outbounds) overlaid on top of it - the
// template itself is loaded once per request from a small file (cheap;
// no caching complexity for what is not a hot path compared to actual
// proxy traffic) but must never be mutated in place, since Go map
// assignment shares the underlying storage.
func shallowCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
