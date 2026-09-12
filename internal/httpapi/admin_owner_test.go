package httpapi

import (
	"net/http"
	"testing"
)

// createSudoAdmin is a small helper: the bootstrap sudo token (owner too,
// see newTestRouter) creates a real DB-backed sudo admin and logs in as
// them, returning their own token for the caller to act as that identity.
func createSudoAdmin(t *testing.T, router http.Handler, ownerToken, username, password string) string {
	t.Helper()
	resp := doRequest(t, router, "POST", "/api/admin", ownerToken, map[string]interface{}{
		"username": username, "password": password, "is_sudo": true,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("create sudo admin %s: %d %v", username, resp.Code, resp.Body)
	}
	return loginAs(t, router, username, password)
}

func TestOwnerCanRevokeSudoFromAnotherSudoAdmin(t *testing.T) {
	router, ownerToken := newTestRouter(t)
	createSudoAdmin(t, router, ownerToken, "target-sudo", "pw123456")

	resp := doRequest(t, router, "PUT", "/api/admin/target-sudo", ownerToken, map[string]interface{}{"is_sudo": false})
	if resp.Code != http.StatusOK {
		t.Fatalf("owner revoking sudo: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["is_sudo"] != false {
		t.Errorf("is_sudo = %v, want false", resp.Body["is_sudo"])
	}
}

func TestNonOwnerSudoCannotTouchAnotherSudoAdminsAccountAtAll(t *testing.T) {
	router, ownerToken := newTestRouter(t)
	adminAToken := createSudoAdmin(t, router, ownerToken, "admin-a", "pw123456")
	createSudoAdmin(t, router, ownerToken, "admin-b", "pw123456")

	resp := doRequest(t, router, "PUT", "/api/admin/admin-b", adminAToken, map[string]interface{}{"is_sudo": false})
	if resp.Code != http.StatusForbidden {
		t.Errorf("non-owner sudo editing another sudo admin: got %d, want 403 (%v)", resp.Code, resp.Body)
	}
}

func TestNonOwnerSudoCannotRevokeTheirOwnSudoAccess(t *testing.T) {
	router, ownerToken := newTestRouter(t)
	selfToken := createSudoAdmin(t, router, ownerToken, "self-sudo", "pw123456")

	resp := doRequest(t, router, "PUT", "/api/admin/self-sudo", selfToken, map[string]interface{}{"is_sudo": false})
	if resp.Code != http.StatusForbidden {
		t.Errorf("self-revoke without owner: got %d, want 403 (%v)", resp.Code, resp.Body)
	}
	if resp.Body["detail"] != "Only an owner can revoke sudo access" {
		t.Errorf("detail = %v", resp.Body["detail"])
	}
}

// TestSettingIsSudoFalseOnANonSudoAdminIsAHarmlessNoOp is a regression test:
// the AdminForm's checkbox always sends is_sudo (true or false, never
// omitted), so a non-owner sudo admin saving an ordinary reseller who is
// already non-sudo must not get rejected just because the wire value is
// literally `false` - only an ACTUAL demotion (already sudo -> not sudo)
// requires owner permission.
func TestSettingIsSudoFalseOnANonSudoAdminIsAHarmlessNoOp(t *testing.T) {
	router, ownerToken := newTestRouter(t)
	editorToken := createSudoAdmin(t, router, ownerToken, "editor", "pw123456")
	resp := doRequest(t, router, "POST", "/api/admin", ownerToken, map[string]interface{}{
		"username": "reseller1", "password": "pw123456", "is_sudo": false,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("create reseller1: %d %v", resp.Code, resp.Body)
	}

	edit := doRequest(t, router, "PUT", "/api/admin/reseller1", editorToken, map[string]interface{}{
		"is_sudo": false, "telegram_id": 555,
	})
	if edit.Code != http.StatusOK {
		t.Fatalf("non-owner editing an already-non-sudo admin: %d %v", edit.Code, edit.Body)
	}
	if edit.Body["is_sudo"] != false {
		t.Errorf("is_sudo = %v, want false (unchanged)", edit.Body["is_sudo"])
	}
}

func TestOwnerCanGrantOwnerAccessAndNonOwnerCannot(t *testing.T) {
	router, ownerToken := newTestRouter(t)
	nonOwnerSudoToken := createSudoAdmin(t, router, ownerToken, "plain-sudo", "pw123456")
	doRequest(t, router, "POST", "/api/admin", ownerToken, map[string]interface{}{
		"username": "future-owner", "password": "pw123456", "is_sudo": true,
	})

	// A non-owner (even sudo) can't touch is_owner at all.
	blocked := doRequest(t, router, "PUT", "/api/admin/future-owner", nonOwnerSudoToken, map[string]interface{}{"is_owner": true})
	if blocked.Code != http.StatusForbidden {
		t.Errorf("non-owner granting owner: got %d, want 403 (%v)", blocked.Code, blocked.Body)
	}

	granted := doRequest(t, router, "PUT", "/api/admin/future-owner", ownerToken, map[string]interface{}{"is_owner": true})
	if granted.Code != http.StatusOK {
		t.Fatalf("owner granting owner: %d %v", granted.Code, granted.Body)
	}
	if granted.Body["is_owner"] != true {
		t.Errorf("is_owner = %v, want true", granted.Body["is_owner"])
	}
}

func TestOwnerCannotRemoveTheirOwnOwnerAccess(t *testing.T) {
	router, ownerToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/admin", ownerToken, map[string]interface{}{
		"username": "second-owner", "password": "pw123456", "is_sudo": true,
	})
	promote := doRequest(t, router, "PUT", "/api/admin/second-owner", ownerToken, map[string]interface{}{"is_owner": true})
	if promote.Code != http.StatusOK {
		t.Fatalf("promote second-owner: %d %v", promote.Code, promote.Body)
	}
	secondOwnerToken := loginAs(t, router, "second-owner", "pw123456")

	selfDemote := doRequest(t, router, "PUT", "/api/admin/second-owner", secondOwnerToken, map[string]interface{}{"is_owner": false})
	if selfDemote.Code != http.StatusForbidden {
		t.Errorf("self-removing owner access: got %d, want 403 (%v)", selfDemote.Code, selfDemote.Body)
	}

	// A DIFFERENT owner removing someone else's owner access is fine.
	otherRemoves := doRequest(t, router, "PUT", "/api/admin/second-owner", ownerToken, map[string]interface{}{"is_owner": false})
	if otherRemoves.Code != http.StatusOK {
		t.Errorf("another owner removing second-owner's access: got %d, want 200 (%v)", otherRemoves.Code, otherRemoves.Body)
	}
}
