package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hashed, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hashed == "correct horse battery staple" {
		t.Fatal("HashPassword returned the plaintext unchanged")
	}
	if !VerifyPassword("correct horse battery staple", hashed) {
		t.Error("VerifyPassword rejected the correct password")
	}
	if VerifyPassword("wrong password", hashed) {
		t.Error("VerifyPassword accepted an incorrect password")
	}
}
