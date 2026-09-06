package httpapi

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/subscription"
)

func TestCustomerCanCreateAndReplyToTicket(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "ticket_customer_1", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
	subToken := subscription.CreateToken("ticket_customer_1", []byte(testSubSecret))

	resp = doRequest(t, router, "POST", "/sub/"+subToken+"/tickets", "", map[string]interface{}{
		"subject": "Cannot connect", "message": "My VPN stopped working today.",
	})
	if resp.Code != 200 {
		t.Fatalf("create ticket: %d %v", resp.Code, resp.Body)
	}
	ticketID := int(resp.Body["id"].(float64))
	if resp.Body["status"] != "open" {
		t.Errorf("status = %v, want open", resp.Body["status"])
	}
	msgs := resp.Body["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 initial message, got %d", len(msgs))
	}
	if _, hasUsername := resp.Body["username"]; hasUsername {
		t.Error("customer-facing ticket response must not carry owner/username info")
	}

	resp = doRequest(t, router, "POST", "/sub/"+subToken+"/tickets/"+strconv.Itoa(ticketID)+"/messages", "", map[string]interface{}{
		"body": "Still broken, any update?",
	})
	if resp.Code != 200 {
		t.Fatalf("customer reply: %d %v", resp.Code, resp.Body)
	}
	msgs = resp.Body["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after reply, got %d", len(msgs))
	}

	resp = doRequest(t, router, "GET", "/sub/"+subToken+"/tickets", "", nil)
	if resp.Code != 200 {
		t.Fatalf("list my tickets: %d %v", resp.Code, resp.Body)
	}
}

func TestCustomerCannotAccessAnothersTicket(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "ticket_owner_a", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "ticket_owner_b", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	subA := subscription.CreateToken("ticket_owner_a", []byte(testSubSecret))
	subB := subscription.CreateToken("ticket_owner_b", []byte(testSubSecret))

	resp := doRequest(t, router, "POST", "/sub/"+subA+"/tickets", "", map[string]interface{}{
		"subject": "A's private ticket", "message": "hello",
	})
	ticketID := int(resp.Body["id"].(float64))

	resp = doRequest(t, router, "POST", "/sub/"+subB+"/tickets/"+strconv.Itoa(ticketID)+"/messages", "", map[string]interface{}{"body": "sneaky"})
	if resp.Code != 404 {
		t.Fatalf("expected 404 for a ticket owned by a different user, got %d %v", resp.Code, resp.Body)
	}
	if resp.Raw == nil || !strings.Contains(string(resp.Raw), "Ticket not found") {
		t.Errorf("expected 'Ticket not found' body, got: %s", resp.Raw)
	}
}

func TestCustomerOpenTicketCapEnforced(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "ticket_cap_user", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("ticket_cap_user", []byte(testSubSecret))

	for i := 0; i < maxOpenTicketsPerUser; i++ {
		resp := doRequest(t, router, "POST", "/sub/"+subToken+"/tickets", "", map[string]interface{}{
			"subject": "ticket " + strconv.Itoa(i), "message": "body",
		})
		if resp.Code != 200 {
			t.Fatalf("create ticket #%d: %d %v", i, resp.Code, resp.Body)
		}
	}

	resp := doRequest(t, router, "POST", "/sub/"+subToken+"/tickets", "", map[string]interface{}{
		"subject": "one too many", "message": "body",
	})
	if resp.Code != 400 {
		t.Fatalf("expected 400 once at the open-ticket cap, got %d %v", resp.Code, resp.Body)
	}
}

func TestAdminReplyReopensClosedTicketAndScopesToOwner(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", sudoToken, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "ticket-reseller-a", "password": "pw12345", "is_sudo": false})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "ticket-reseller-b", "password": "pw12345", "is_sudo": false})
	resellerA := loginAs(t, router, "ticket-reseller-a", "pw12345")
	resellerB := loginAs(t, router, "ticket-reseller-b", "pw12345")

	doRequest(t, router, "POST", "/api/user", resellerA, map[string]interface{}{
		"username": "reseller_a_customer", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("reseller_a_customer", []byte(testSubSecret))
	resp := doRequest(t, router, "POST", "/sub/"+subToken+"/tickets", "", map[string]interface{}{
		"subject": "help", "message": "please help",
	})
	ticketID := int(resp.Body["id"].(float64))

	// Reseller B must not see reseller A's customer's ticket.
	resp = doRequest(t, router, "GET", "/api/tickets/"+strconv.Itoa(ticketID), resellerB, nil)
	if resp.Code != 404 {
		t.Fatalf("expected 404 for a ticket outside reseller B's scope, got %d %v", resp.Code, resp.Body)
	}

	// Reseller A can see and reply, and its owner/username fields are correct.
	resp = doRequest(t, router, "GET", "/api/tickets/"+strconv.Itoa(ticketID), resellerA, nil)
	if resp.Code != 200 {
		t.Fatalf("reseller A get ticket: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["username"] != "reseller_a_customer" {
		t.Errorf("username = %v, want reseller_a_customer", resp.Body["username"])
	}
	if resp.Body["owner"] != "ticket-reseller-a" {
		t.Errorf("owner = %v, want ticket-reseller-a", resp.Body["owner"])
	}

	// Close it, then reply as the admin - must reopen.
	resp = doRequest(t, router, "PUT", "/api/tickets/"+strconv.Itoa(ticketID), resellerA, map[string]interface{}{"status": "closed"})
	if resp.Code != 200 || resp.Body["status"] != "closed" {
		t.Fatalf("close ticket: %d %v", resp.Code, resp.Body)
	}
	resp = doRequest(t, router, "POST", "/api/tickets/"+strconv.Itoa(ticketID)+"/messages", resellerA, map[string]interface{}{"body": "we're on it"})
	if resp.Code != 200 {
		t.Fatalf("admin reply: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["status"] != "open" {
		t.Errorf("status after admin reply = %v, want open (reply must reopen a closed ticket)", resp.Body["status"])
	}

	// Sudo sees it too, unscoped.
	resp = doRequest(t, router, "GET", "/api/tickets", sudoToken, nil)
	if resp.Code != 200 {
		t.Fatalf("sudo list tickets: %d %v", resp.Code, resp.Body)
	}
}

func TestTicketMessageCapEnforced(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "ticket_msgcap_user", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("ticket_msgcap_user", []byte(testSubSecret))
	resp := doRequest(t, router, "POST", "/sub/"+subToken+"/tickets", "", map[string]interface{}{
		"subject": "chatty", "message": "msg 0",
	})
	ticketID := int(resp.Body["id"].(float64))

	for i := 1; i < maxMessagesPerTicket; i++ {
		resp := doRequest(t, router, "POST", "/sub/"+subToken+"/tickets/"+strconv.Itoa(ticketID)+"/messages", "", map[string]interface{}{"body": "msg"})
		if resp.Code != 200 {
			t.Fatalf("reply #%d: %d %v", i, resp.Code, resp.Body)
		}
	}
	resp = doRequest(t, router, "POST", "/sub/"+subToken+"/tickets/"+strconv.Itoa(ticketID)+"/messages", "", map[string]interface{}{"body": "one too many"})
	if resp.Code != 400 {
		t.Fatalf("expected 400 once at the per-ticket message cap, got %d %v", resp.Code, resp.Body)
	}
}

func TestEmergencyRechargeGrantsBonusAndReactivates(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "emergency_user_1", "data_limit": 1000, "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	// expire is a raw unix timestamp on this endpoint (not "days") - setting
	// it to 1 (1970) puts the user unambiguously in the past, exercising the
	// eligibility guard's "expired" branch.
	doRequest(t, router, "PUT", "/api/user/emergency_user_1", token, map[string]interface{}{"expire": 1})
	resp := doRequest(t, router, "GET", "/api/user/emergency_user_1", token, nil)
	if resp.Body["status"] != "expired" {
		t.Fatalf("expected user to be expired for this test, got %v", resp.Body["status"])
	}

	beforeRecharge := time.Now().Unix()
	subToken := subscription.CreateToken("emergency_user_1", []byte(testSubSecret))
	resp = doRequest(t, router, "POST", "/sub/"+subToken+"/emergency", "", nil)
	if resp.Code != 200 {
		t.Fatalf("emergency recharge: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["status"] != "active" {
		t.Errorf("status after recharge = %v, want active", resp.Body["status"])
	}
	// New expire must be ~now+1800s (GREATEST(expire, now()) + 1800), not
	// old-expire(1)+1800 - a wide-but-real window around the actual grant,
	// not just "any timestamp bigger than a tiny constant".
	newExpire := int64(resp.Body["expire"].(float64))
	wantMin, wantMax := beforeRecharge+emergencyRechargeSeconds-5, time.Now().Unix()+emergencyRechargeSeconds+5
	if newExpire < wantMin || newExpire > wantMax {
		t.Errorf("expire = %d, want between %d and %d (now + 30 minutes)", newExpire, wantMin, wantMax)
	}

	// Second attempt must fail - one-time only.
	resp = doRequest(t, router, "POST", "/sub/"+subToken+"/emergency", "", nil)
	if resp.Code != 409 {
		t.Fatalf("expected 409 on a second emergency recharge attempt, got %d %v", resp.Code, resp.Body)
	}
}

func TestEmergencyRechargeRejectsIneligibleUser(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "emergency_ineligible_user", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("emergency_ineligible_user", []byte(testSubSecret))

	resp := doRequest(t, router, "POST", "/sub/"+subToken+"/emergency", "", nil)
	if resp.Code != 400 {
		t.Fatalf("expected 400 for an active (not limited/expired) user, got %d %v", resp.Code, resp.Body)
	}
}

func TestEmergencyRechargeRaceIsSafe(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "emergency_race_user", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	doRequest(t, router, "PUT", "/api/user/emergency_race_user", token, map[string]interface{}{"expire": 1})
	subToken := subscription.CreateToken("emergency_race_user", []byte(testSubSecret))

	const attempts = 10
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := doRequest(t, router, "POST", "/sub/"+subToken+"/emergency", "", nil)
			codes[i] = resp.Code
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, code := range codes {
		if code == 200 {
			successes++
		} else if code != 409 {
			t.Errorf("unexpected status code from concurrent emergency recharge: %d", code)
		}
	}
	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1 (the grant must never be claimed twice)", successes)
	}
}
