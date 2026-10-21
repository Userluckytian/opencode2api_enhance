package main

import (
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h := HashPassword("secret123")
	if h == "secret123" {
		t.Fatal("must not store plaintext")
	}
	if !VerifyPassword(h, "secret123") {
		t.Fatal("correct password must verify")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatal("wrong password must fail")
	}
}

func TestHashUniqueSalts(t *testing.T) {
	if HashPassword("secret123") == HashPassword("secret123") {
		t.Fatal("salts should differ")
	}
}

func TestSessionCreateAndValid(t *testing.T) {
	s := NewSessionStore()
	tok := s.Create(time.Hour)
	if !s.Valid(tok, time.Now().Add(30*time.Minute)) {
		t.Fatal("token should be valid")
	}
	if s.Valid(tok, time.Now().Add(2*time.Hour)) {
		t.Fatal("token should expire")
	}
	s.Revoke(tok)
	if s.Valid(tok, time.Now()) {
		t.Fatal("revoked token must be invalid")
	}
}
