package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const pbkdf2Iter = 10000

func hashOnce(salt, pw string) []byte {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(pw))
	return h.Sum(nil)
}

func HashPassword(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	saltHex := hex.EncodeToString(salt)
	prev := hashOnce(saltHex, pw)
	for i := 1; i < pbkdf2Iter; i++ {
		h := sha256.New()
		h.Write(prev)
		h.Write([]byte(saltHex))
		prev = h.Sum(nil)
	}
	return saltHex + "$" + hex.EncodeToString(prev)
}

func VerifyPassword(hash, pw string) bool {
	parts := strings.SplitN(hash, "$", 2)
	if len(parts) != 2 {
		return false
	}
	saltHex := parts[0]
	want, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	prev := hashOnce(saltHex, pw)
	for i := 1; i < pbkdf2Iter; i++ {
		h := sha256.New()
		h.Write(prev)
		h.Write([]byte(saltHex))
		prev = h.Sum(nil)
	}
	return subtle.ConstantTimeCompare(prev, want) == 1
}

type SessionStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time
}

func NewSessionStore() *SessionStore {
	return &SessionStore{tokens: map[string]time.Time{}}
}

func (s *SessionStore) Create(ttl time.Duration) string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	tok := hex.EncodeToString(buf)
	s.mu.Lock()
	s.tokens[tok] = time.Now().Add(ttl)
	s.mu.Unlock()
	return tok
}

func (s *SessionStore) Valid(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	exp, ok := s.tokens[token]
	s.mu.Unlock()
	if !ok {
		return false
	}
	if now.After(exp) {
		s.mu.Lock()
		delete(s.tokens, token)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *SessionStore) Revoke(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

// RevokeAllExcept 吊销除 keep 外的全部会话；改密码后调用，保留当前操作者登录态。
func (s *SessionStore) RevokeAllExcept(keep string) {
	s.mu.Lock()
	for tok := range s.tokens {
		if tok != keep {
			delete(s.tokens, tok)
		}
	}
	s.mu.Unlock()
}