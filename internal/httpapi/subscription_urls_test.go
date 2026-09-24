package httpapi

import (
	"reflect"
	"strings"
	"testing"
)

func stringList(t *testing.T, v interface{}) []string {
	t.Helper()
	arr, ok := v.([]interface{})
	if !ok {
		t.Fatalf("not a JSON array: %#v", v)
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		out = append(out, x.(string))
	}
	return out
}

// A user's subscription answers on every configured address. The dashboard
// lists them in the configured order; the single `subscription_url` that
// reseller bots read must not move.
func TestUserResponseListsEverySubscriptionAddressInConfiguredOrder(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.subURLPrefix = "https://sub.officialvpn.shop"
	handler.WithSubscriptionURLPrefixes([]string{"https://sub.ts01.ir/", "https://sub.officialvpn.shop"})

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "SubURLs VLESS", "protocol": "vless"}})
	created := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sub_urls_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	single, _ := created.Body["subscription_url"].(string)
	if !strings.HasPrefix(single, "https://sub.officialvpn.shop/sub/") {
		t.Fatalf("subscription_url = %q, want it unchanged on the old address", single)
	}
	tok := single[strings.LastIndex(single, "/")+1:]

	want := []string{"https://sub.ts01.ir/sub/" + tok, "https://sub.officialvpn.shop/sub/" + tok}
	if got := stringList(t, created.Body["subscription_urls"]); !reflect.DeepEqual(got, want) {
		t.Errorf("create: subscription_urls = %v, want %v (trailing slash trimmed, new address first)", got, want)
	}

	list := doRequest(t, router, "GET", "/api/users", token, nil)
	users, _ := list.Body["users"].([]interface{})
	if len(users) == 0 {
		t.Fatalf("no users in list: %s", list.Raw)
	}
	// A token embeds the second it was minted, so the list response carries a
	// fresh one; what must hold is that its two addresses share that token.
	first := users[0].(map[string]interface{})
	listSingle, _ := first["subscription_url"].(string)
	listTok := listSingle[strings.LastIndex(listSingle, "/")+1:]
	wantList := []string{"https://sub.ts01.ir/sub/" + listTok, "https://sub.officialvpn.shop/sub/" + listTok}
	if got := stringList(t, first["subscription_urls"]); !reflect.DeepEqual(got, wantList) {
		t.Errorf("list: subscription_urls = %v, want %v", got, wantList)
	}

	// Both addresses carry the same token, so either one opens the same subscription.
	for _, u := range want {
		path := u[strings.Index(u, "/sub/"):]
		resp := doRequest(t, router, "GET", path, "", nil)
		if resp.Code != 200 {
			t.Errorf("GET %s = %d, want 200", path, resp.Code)
		}
	}
}

func TestUserResponseSubscriptionAddressListFallsBackToTheSingleAddress(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.subURLPrefix = "https://sub.officialvpn.shop"

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "SubURLs VLESS", "protocol": "vless"}})
	created := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sub_urls_user2", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	single, _ := created.Body["subscription_url"].(string)
	got := stringList(t, created.Body["subscription_urls"])
	if !reflect.DeepEqual(got, []string{single}) {
		t.Errorf("subscription_urls = %v, want just [%s] when no list is configured", got, single)
	}
}
