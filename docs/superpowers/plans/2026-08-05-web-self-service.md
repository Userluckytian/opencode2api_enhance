# 网页自助领 Key 服务（Web Self-Service）Implementation Plan

> **端口更新（2026-08-06）：三环境端口隔离后，web 服务端口已由 18081 改为 23081**（下文中的 18081 均为历史值）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `feature/web-self-service` 分支上用 Go 实现一个单程序全包的网页自助领 Key 服务：用户浏览器领 Key、管理员网页后台管理、OpenAI 兼容 `/v1` 网关校验 Key 后转发上游。

**Architecture:** 根仓库共享 `go.mod`（module `github.com/6Kmfi6HP/opencode2api`，go 1.22，纯标准库零外部依赖）。新代码放 `web/` 子目录，`package main`，`go:embed` 内嵌静态前端。单一 HTTP 服务监听 18081，标准库 `net/http` + Go 1.22 `http.ServeMux` 方法路由。请求链：`/v1/*` → 校验 Key → 限速 → 转发上游（SSE 透传）→ 解析 usage → 扣额度 + 记日志。数据存数据目录 `web-data/`。

**Tech Stack:** Go ≥ 1.22（本地 go1.26.5/windows/amd64）、标准库 `net/http`、`crypto/rand`、`crypto/sha256`、`crypto/subtle`、`encoding/json`、`embed`、原生 HTML/CSS/JS。测试用标准库 `testing` + `net/http/httptest`。

## 设计文档

见 `docs/superpowers/specs/2026-08-05-web-self-service-design.md`。

## Global Constraints

- 零外部依赖：只用标准库，不改根 `go.mod`，不新增依赖。
- 目录：所有 Go 源文件放 `web/` 子目录（**不改根 `main.go` 及任何协议文件**）。
- 端口：默认 `18081`（经 `config.json` 的 `port` 调整）。
- 数据目录：默认 `./web-data`（可用 `-data-dir` flag 覆盖）。
- 编译产物：`bin/web-server.exe`（`bin/` 已被 `.gitignore` 忽略）。
- 前端全中文，浅色官网风格（底 `#f4f4f5`、白卡片、accent `#0f766e`）。
- 测试命令：`go test ./web/...`；构建命令：`go build -o bin/web-server.exe ./web`。
- 管理员密码只存哈希；Key 用 `sk-` + 32 位随机十六进制。
- 所有提交仅本地，**不 push 远程**。
- Windows PowerShell 环境，所有命令用 PowerShell 语法运行。

---

## 文件结构

```
web/
├── main.go        # 入口：flag、加载配置、启动 HTTP、路由装配
├── config.go      # Config 结构 + 加载/保存 config.json
├── store.go       # Key 存储（keys.json）+ Key 生成
├── ratelimit.go   # 限速器（固定窗口，按 Key）
├── auth.go        # 管理员密码哈希/校验 + 登录令牌
├── models.go      # 从上游 /v1/models 拉取模型列表
├── api.go         # 用户领 Key 接口（/api/models、/api/keys）
├── admin.go       # 管理接口（/api/admin/*）
├── gateway.go     # /v1/* 网关：Key 校验 + 转发 + 用量解析
├── logger.go      # 调用日志（按天 jsonl 追加）
├── config_test.go
├── store_test.go
├── ratelimit_test.go
├── auth_test.go
├── models_test.go
├── api_test.go
├── admin_test.go
├── gateway_test.go
└── static/
    ├── index.html   # 领 Key 页
    ├── admin.html   # 管理后台
    ├── style.css
    ├── app.js       # 领 Key 页逻辑
    └── admin.js     # 管理后台逻辑
```

---

### Task 1: 配置模块 config.go

**Files:**
- Create: `web/config.go`
- Test: `web/config_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { AdminPasswordHash string; UpstreamBaseURL string; TTLDays int; TokenQuota int; RateLimitPerMin int; AllowedModels []string; Port int }`，JSON 字段：`admin_password_hash`、`upstream_base_url`、`ttl_days`、`token_quota`、`rate_limit_per_min`、`allowed_models`、`port`。
  - `func DefaultConfig() *Config`
  - `func LoadConfig(dir string) (*Config, error)`：读 `<dir>/config.json`；文件不存在则写默认值并返回默认配置。
  - `func (c *Config) Save(dir string) error`：写 `<dir>/config.json`（缩进 JSON，创建目录）。

- [ ] **Step 1: 写失败测试**

`web/config_test.go`：
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Port != 18081 {
		t.Fatalf("Port = %d, want 18081", c.Port)
	}
	if c.TTLDays != 7 || c.TokenQuota != 1000000 || c.RateLimitPerMin != 30 {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// 目录为空：LoadConfig 应返回默认值并落盘
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.UpstreamBaseURL != "" {
		t.Fatalf("empty upstream wanted, got %q", c.UpstreamBaseURL)
	}
	c.AdminPasswordHash = "hash"
	c.UpstreamBaseURL = "http://127.0.0.1:18080/v1"
	c.AllowedModels = []string{"glm-4"}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	// 重新加载应从文件读回
	c2, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.UpstreamBaseURL != "http://127.0.0.1:18080/v1" {
		t.Fatalf("roundtrip failed: %+v", c2)
	}
	if len(c2.AllowedModels) != 1 || c2.AllowedModels[0] != "glm-4" {
		t.Fatalf("allowed models roundtrip failed: %+v", c2.AllowedModels)
	}
	_ = os.Remove(filepath.Join(dir, "config.json"))
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestDefaultConfig -v`
Expected: FAIL（`web/config.go` 不存在，编译错误 "no Go files" 或 undefined）。

- [ ] **Step 3: 实现 config.go**

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	AdminPasswordHash string   `json:"admin_password_hash"`
	UpstreamBaseURL   string   `json:"upstream_base_url"`
	TTLDays           int      `json:"ttl_days"`
	TokenQuota        int      `json:"token_quota"`
	RateLimitPerMin   int      `json:"rate_limit_per_min"`
	AllowedModels     []string `json:"allowed_models"`
	Port              int      `json:"port"`
}

func DefaultConfig() *Config {
	return &Config{
		TTLDays:         7,
		TokenQuota:      1000000,
		RateLimitPerMin: 30,
		Port:            18081,
	}
}

func LoadConfig(dir string) (*Config, error) {
	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := DefaultConfig()
		if err := c.Save(dir); err != nil {
			return nil, err
		}
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c := DefaultConfig()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}
```

> 注：`sync.Once` 占位仅为防止空引用误删；若该文件未用 sync 可移除该行与 import。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run TestDefaultConfig -v`
Expected: PASS（`TestDefaultConfig`、`TestLoadConfigRoundTrip` 均通过）。

- [ ] **Step 5: Commit**

Run: `git add web/config.go web/config_test.go; git commit -m "feat(web): 配置模块 config 加载与保存"`
Expected: 提交成功。

---

### Task 2: Key 存储与生成 store.go

**Files:**
- Create: `web/store.go`
- Test: `web/store_test.go`

**Interfaces:**
- Consumes: `time`、`crypto/rand`、`encoding/hex`。
- Produces:
  - `type Key struct { ID string; Key string; Model string; CreatedAt time.Time; ExpiresAt time.Time; TokenUsed int; Quota int; Disabled bool }`，JSON 字段：`id`、`key`、`model`、`created_at`、`expires_at`、`token_used`、`quota`、`disabled`。
  - `func GenerateKey() string`：返回 `sk-` + 32 位随机十六进制。
  - `type Store struct{ ... }`
  - `func NewStore(dir string) (*Store, error)`：加载 `<dir>/keys.json`（不存在则空列表）。
  - `func (s *Store) Save() error`
  - `func (s *Store) CreateKey(model string, ttlDays, quota int, now time.Time) (*Key, error)`：生成新 Key，`ExpiresAt = now + ttlDays*24h`。
  - `func (s *Store) GetByKey(key string) (*Key, bool)`
  - `func (s *Store) AddUsage(id string, tokens int)`
  - `func (s *Store) SetDisabled(id string, disabled bool) error`
  - `func (s *Store) Delete(id string) error`
  - `func (s *Store) GetByID(id string) (*Key, bool)`
  - `func (s *Store) List() []Key`

- [ ] **Step 1: 写失败测试**

`web/store_test.go`：
```go
package main

import (
	"testing"
	"time"
)

func TestGenerateKeyFormat(t *testing.T) {
	k := GenerateKey()
	if len(k) != 3+32 {
		t.Fatalf("len = %d, want 35", len(k))
	}
	if k[:3] != "sk-" {
		t.Fatalf("prefix = %q, want sk-", k[:3])
	}
}

func TestCreateAndGetByKey(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	k, err := s.CreateKey("glm-4", 7, 1000000, now)
	if err != nil {
		t.Fatal(err)
	}
	if k.Model != "glm-4" {
		t.Fatalf("model = %q", k.Model)
	}
	if !k.ExpiresAt.After(now.Add(6 * 24 * time.Hour)) {
		t.Fatalf("expires too early: %v", k.ExpiresAt)
	}
	got, ok := s.GetByKey(k.Key)
	if !ok || got.ID != k.ID {
		t.Fatalf("GetByKey failed: ok=%v got=%+v", ok, got)
	}
}

func TestAddUsageAndQuota(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 1000, now)
	s.AddUsage(k.ID, 400)
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 400 {
		t.Fatalf("TokenUsed = %d, want 400", got.TokenUsed)
	}
	s.AddUsage(k.ID, 100)
	if got, _ := s.GetByID(k.ID); got.TokenUsed != 500 {
		t.Fatalf("TokenUsed = %d, want 500", got.TokenUsed)
	}
}

func TestSetDisabledAndDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 1000, now)
	if err := s.SetDisabled(k.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(k.ID); !got.Disabled {
		t.Fatal("expected disabled")
	}
	if err := s.SetDisabled(k.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByID(k.ID); got.Disabled {
		t.Fatal("expected enabled")
	}
	if err := s.Delete(k.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetByID(k.ID); ok {
		t.Fatal("expected deleted")
	}
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	now := time.Now()
	k, _ := s.CreateKey("m", 7, 1000, now)
	s.AddUsage(k.ID, 123) // 触发写盘
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.GetByKey(k.Key)
	if !ok {
		t.Fatal("key not persisted")
	}
	if got.TokenUsed != 123 {
		t.Fatalf("TokenUsed not persisted: %d", got.TokenUsed)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestGenerateKeyFormat -v`
Expected: FAIL（`store.go` 不存在）。

- [ ] **Step 3: 实现 store.go**

```go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Key struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	TokenUsed int       `json:"token_used"`
	Quota     int       `json:"quota"`
	Disabled  bool      `json:"disabled"`
}

func GenerateKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "sk-" + hex.EncodeToString(buf)
}

type Store struct {
	mu   sync.Mutex
	dir  string
	keys map[string]*Key // by ID
}

func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir, keys: map[string]*Key{}}
	data, err := os.ReadFile(filepath.Join(dir, "keys.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Key
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, k := range list {
		s.keys[k.ID] = k
	}
	return s, nil
}

func (s *Store) filePath() string { return filepath.Join(s.dir, "keys.json") }

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked()
}

func (s *Store) writeLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	list := make([]*Key, 0, len(s.keys))
	for _, k := range s.keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(), data, 0o644)
}

func (s *Store) CreateKey(model string, ttlDays, quota int, now time.Time) (*Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &Key{
		ID:        GenerateKey(),
		Key:       GenerateKey(),
		Model:     model,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(ttlDays) * 24 * time.Hour),
		TokenUsed: 0,
		Quota:     quota,
		Disabled:  false,
	}
	s.keys[k.ID] = k
	return k, s.writeLocked()
}

func (s *Store) GetByKey(key string) (*Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.Key == key {
			return k, true
		}
	}
	return nil, false
}

func (s *Store) GetByID(id string) (*Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	return k, ok
}

func (s *Store) AddUsage(id string, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys[id]; ok {
		k.TokenUsed += tokens
		_ = s.writeLocked()
	}
}

func (s *Store) SetDisabled(id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[id]
	if !ok {
		return os.ErrNotExist
	}
	k.Disabled = disabled
	return s.writeLocked()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.keys, id)
	return s.writeLocked()
}

func (s *Store) List() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, *k)
	}
	return out
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run 'TestGenerateKeyFormat|TestCreateAndGetByKey|TestAddUsageAndQuota|TestSetDisabledAndDelete|TestStorePersists' -v`
Expected: PASS（全部通过）。

- [ ] **Step 5: Commit**

Run: `git add web/store.go web/store_test.go; git commit -m "feat(web): Key 存储与生成 store"`
Expected: 提交成功。

---

### Task 3: 限速器 ratelimit.go

**Files:**
- Create: `web/ratelimit.go`
- Test: `web/ratelimit_test.go`

**Interfaces:**
- Produces:
  - `type RateLimiter struct{ ... }`
  - `func NewRateLimiter(perMin int) *RateLimiter`：固定窗口限速，窗口 60 秒，每个 Key 一个计数。
  - `func (r *RateLimiter) Allow(key string, now time.Time) bool`：该 Key 在本窗口内已达上限返回 false，否则计数 +1 返回 true。
  - 注意：`perMin <= 0` 表示不限速（Allow 恒 true）。

- [ ] **Step 1: 写失败测试**

`web/ratelimit_test.go`：
```go
package main

import (
	"testing"
	"time"
)

func TestRateLimitBlocksAfterLimit(t *testing.T) {
	rl := NewRateLimiter(3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !rl.Allow("key1", now) {
			t.Fatalf("allow %d should pass", i)
		}
	}
	if rl.Allow("key1", now) {
		t.Fatal("4th request should be blocked")
	}
}

func TestRateLimitIsolationPerKey(t *testing.T) {
	rl := NewRateLimiter(2)
	now := time.Now()
	rl.Allow("a", now)
	rl.Allow("a", now)
	if rl.Allow("a", now) {
		t.Fatal("a should be blocked")
	}
	if !rl.Allow("b", now) {
		t.Fatal("b should be allowed independently")
	}
}

func TestRateLimitWindowReset(t *testing.T) {
	rl := NewRateLimiter(2)
	now := time.Now()
	rl.Allow("k", now)
	rl.Allow("k", now)
	if rl.Allow("k", now) {
		t.Fatal("blocked within window")
	}
	// 下一窗口
	if !rl.Allow("k", now.Add(61*time.Second)) {
		t.Fatal("should reset in next window")
	}
}

func TestRateLimitZeroMeansUnlimited(t *testing.T) {
	rl := NewRateLimiter(0)
	now := time.Now()
	for i := 0; i < 1000; i++ {
		if !rl.Allow("k", now) {
			t.Fatal("0 means unlimited")
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestRateLimit -v`
Expected: FAIL。

- [ ] **Step 3: 实现 ratelimit.go**

```go
package main

import (
	"sync"
	"time"
)

type window struct {
	start int64 // 窗口起始 Unix 秒
	count int
}

type RateLimiter struct {
	mu      sync.Mutex
	perMin  int
	buckets map[string]*window
}

func NewRateLimiter(perMin int) *RateLimiter {
	return &RateLimiter{perMin: perMin, buckets: map[string]*window{}}
}

func (r *RateLimiter) Allow(key string, now time.Time) bool {
	if r.perMin <= 0 {
		return true
	}
	sec := now.Unix()
	win := sec / 60
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[key]
	if !ok || b.start != win {
		r.buckets[key] = &window{start: win, count: 1}
		return true
	}
	if b.count >= r.perMin {
		return false
	}
	b.count++
	return true
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run TestRateLimit -v`
Expected: PASS。

- [ ] **Step 5: Commit**

Run: `git add web/ratelimit.go web/ratelimit_test.go; git commit -m "feat(web): 固定窗口限速器 ratelimit"`
Expected: 提交成功。

---

### Task 4: 管理员认证 auth.go

**Files:**
- Create: `web/auth.go`
- Test: `web/auth_test.go`

**Interfaces:**
- Produces:
  - `func HashPassword(pw string) string`：PBKDF2 风格，标准库实现：`salt(16B hex) + "$" + iter(10000)次 SHA-256(salt + pw + prev)`。
  - `func VerifyPassword(hash, pw string) bool`
  - `type SessionStore struct{ ... }`
  - `func NewSessionStore() *SessionStore`：内存令牌表（token → 过期 time）。
  - `func (s *SessionStore) Create(ttl time.Duration) string`：生成随机令牌，记录过期时间，返回令牌。
  - `func (s *SessionStore) Valid(token string, now time.Time) bool`
  - `func (s *SessionStore) Revoke(token string)`

- [ ] **Step 1: 写失败测试**

`web/auth_test.go`：
```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestHashAndVerify -v`
Expected: FAIL。

- [ ] **Step 3: 实现 auth.go**

```go
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run TestHashAndVerify -v`
Expected: PASS。

- [ ] **Step 5: Commit**

Run: `git add web/auth.go web/auth_test.go; git commit -m "feat(web): 管理员密码哈希与登录令牌 auth"`
Expected: 提交成功。

---

### Task 5: 上游模型拉取 models.go

**Files:**
- Create: `web/models.go`
- Test: `web/models_test.go`

**Interfaces:**
- Consumes: `http.Client`。
- Produces:
  - `func FetchModels(upstreamBase string, client *http.Client) ([]string, error)`：`GET <upstreamBase>/models`（注意 `upstreamBase` 形如 `http://127.0.0.1:18080/v1`），解析响应 `{"data":[{"id":"..."},...]}`，返回 id 列表（去掉空 id）。

- [ ] **Step 1: 写失败测试**

`web/models_test.go`：
```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "glm-4"}, {"id": "kimi-k2"}, {"id": ""}},
		})
	}))
	defer srv.Close()
	models, err := FetchModels(srv.URL+"/v1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "glm-4" || models[1] != "kimi-k2" {
		t.Fatalf("models = %v", models)
	}
}

func TestFetchModelsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := FetchModels(srv.URL+"/v1", srv.Client()); err == nil {
		t.Fatal("expected error on upstream failure")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestFetchModels -v`
Expected: FAIL。

- [ ] **Step 3: 实现 models.go**

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func FetchModels(upstreamBase string, client *http.Client) ([]string, error) {
	if !strings.HasSuffix(upstreamBase, "/") {
		upstreamBase += "/"
	}
	url := upstreamBase + "models"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream /models status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range payload.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run TestFetchModels -v`
Expected: PASS。

- [ ] **Step 5: Commit**

Run: `git add web/models.go web/models_test.go; git commit -m "feat(web): 从上游拉取模型列表 models"`
Expected: 提交成功。

---

### Task 6: 用户领 Key 接口 api.go

**Files:**
- Create: `web/api.go`
- Test: `web/api_test.go`

**Interfaces:**
- Consumes: `Config`（Task 1）、`Store`（Task 2）。
- Produces:
  - `type App struct { Cfg *Config; Store *Store; Rl *RateLimiter; Sessions *SessionStore; Logger *Logger; Client *http.Client; ConfigPath string }`（`ConfigPath` 为配置保存路径，等于数据目录；`Logger` 与 `Client` 由 Task 7 填充，本任务先声明字段）。
  - `func (a *App) HandleModels(w http.ResponseWriter, r *http.Request)`：`GET /api/models`，返回 `{"models": a.Cfg.AllowedModels}`。
  - `func (a *App) HandleCreateKey(w http.ResponseWriter, r *http.Request)`：`POST /api/keys`，body `{"model":"..."}`；模型不在白名单返回 400 `{"error":"model not allowed"}`；成功返回 200 `{"base_url":"http://<host>/v1","key":"sk-...","expires_at":"RFC3339","model":"..."}`。

- [ ] **Step 1: 写失败测试**

`web/api_test.go`：
```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AllowedModels = []string{"glm-4", "kimi-k2"}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{Cfg: cfg, Store: store}
}

func TestHandleModels(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.HandleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 2 || body.Models[0] != "glm-4" {
		t.Fatalf("models = %v", body.Models)
	}
}

func TestHandleCreateKey(t *testing.T) {
	app := newTestApp(t)
	reqBody := bytes.NewBufferString(`{"model":"glm-4"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", reqBody)
	req.Host = "example.com:18081"
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		BaseURL   string `json:"base_url"`
		Key       string `json:"key"`
		ExpiresAt string `json:"expires_at"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.BaseURL != "http://example.com:18081/v1" {
		t.Fatalf("base_url = %q", body.BaseURL)
	}
	if body.Key[:3] != "sk-" {
		t.Fatalf("key = %q", body.Key)
	}
	if body.Model != "glm-4" {
		t.Fatalf("model = %q", body.Model)
	}
	// Key 应已入库且绑定模型
	if _, ok := app.Store.GetByKey(body.Key); !ok {
		t.Fatal("key not stored")
	}
}

func TestHandleCreateKeyRejectsNonWhitelisted(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`{"model":"gpt-4"}`))
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleCreateKeyBadJSON(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString(`not-json`))
	rec := httptest.NewRecorder()
	app.HandleCreateKey(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestHandleModels -v`
Expected: FAIL。

- [ ] **Step 3: 实现 api.go**

```go
package main

import (
	"encoding/json"
	"net/http"
	"time"
)

type App struct {
	Cfg        *Config
	Store      *Store
	Rl         *RateLimiter
	Sessions   *SessionStore
	Logger     *Logger
	Client     *http.Client
	ConfigPath string // 数据目录（config.json 所在目录）
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *App) HandleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"models": a.Cfg.AllowedModels})
}

func (a *App) HandleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !contains(a.Cfg.AllowedModels, req.Model) {
		writeErr(w, http.StatusBadRequest, "model not allowed")
		return
	}
	k, err := a.Store.CreateKey(req.Model, a.Cfg.TTLDays, a.Cfg.TokenQuota, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create key")
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":   scheme + "://" + r.Host + "/v1",
		"key":        k.Key,
		"expires_at": k.ExpiresAt.Format(time.RFC3339),
		"model":      k.Model,
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run TestHandle -v`
Expected: PASS。

- [ ] **Step 5: Commit**

Run: `git add web/api.go web/api_test.go; git commit -m "feat(web): 用户领 Key 接口 api"`
Expected: 提交成功。

---

### Task 7: 调用日志 + /v1 网关 logger.go / gateway.go

**Files:**
- Create: `web/logger.go`、`web/gateway.go`
- Test: `web/logger_test.go`、`web/gateway_test.go`

**Interfaces:**
- Consumes: `Config`、`Store`、`RateLimiter`、`App`（Task 6）。
- Produces:
  - `type LogEntry struct { Time string; KeyID string; Model string; PromptTokens int; CompletionTokens int; Total int; Status int; Ms int64 }`，JSON 字段：`time`、`key_id`、`model`、`prompt_tokens`、`completion_tokens`、`total`、`status`、`ms`。
  - `func NewLogger(dir string) *Logger`
  - `func (l *Logger) Write(e LogEntry) error`：追加到 `<dir>/logs/log-YYYY-MM-DD.jsonl`（按 `e.Time` 的日期分文件，`os.OpenFile` append）。
  - `func (l *Logger) ReadLogs(date, keyID string) ([]LogEntry, error)`：读 `log-<date>.jsonl`；`keyID` 非空则过滤；文件不存在返回空列表。
  - `func (a *App) HandleV1(w http.ResponseWriter, r *http.Request)`：`/v1/*` 网关入口。

- [ ] **Step 1: 写失败测试（logger）**

`web/logger_test.go`：
```go
package main

import (
	"testing"
	"time"
)

func TestLoggerWriteAndRead(t *testing.T) {
	l := NewLogger(t.TempDir())
	now := time.Now()
	if err := l.Write(LogEntry{Time: now.Format(time.RFC3339), KeyID: "k1", Model: "glm-4", Total: 100, Status: 200, Ms: 5}); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(LogEntry{Time: now.Format(time.RFC3339), KeyID: "k2", Model: "kimi-k2", Total: 50, Status: 200, Ms: 3}); err != nil {
		t.Fatal(err)
	}
	date := now.Format("2006-01-02")
	all, err := l.ReadLogs(date, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	onlyK1, err := l.ReadLogs(date, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyK1) != 1 || onlyK1[0].KeyID != "k1" {
		t.Fatalf("filter failed: %+v", onlyK1)
	}
	none, err := l.ReadLogs("2099-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("missing file should give empty list")
	}
}
```

- [ ] **Step 2: 跑 logger 测试确认失败**

Run: `go test ./web/ -run TestLoggerWriteAndRead -v`
Expected: FAIL。

- [ ] **Step 3: 实现 logger.go**

```go
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LogEntry struct {
	Time             string `json:"time"`
	KeyID            string `json:"key_id"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Total            int    `json:"total"`
	Status           int    `json:"status"`
	Ms               int64  `json:"ms"`
}

type Logger struct {
	mu  sync.Mutex
	dir string
}

func NewLogger(dir string) *Logger {
	return &Logger{dir: filepath.Join(dir, "logs")}
}

func (l *Logger) logPath(t time.Time) string {
	return filepath.Join(l.dir, "log-"+t.Format("2006-01-02")+".jsonl")
}

func (l *Logger) Write(e LogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t = time.Now()
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.logPath(t), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func (l *Logger) ReadLogs(date, keyID string) ([]LogEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(filepath.Join(l.dir, "log-"+date+".jsonl"))
	if os.IsNotExist(err) {
		return []LogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if keyID != "" && e.KeyID != keyID {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
```

- [ ] **Step 4: 跑 logger 测试确认通过**

Run: `go test ./web/ -run TestLoggerWriteAndRead -v`
Expected: PASS。

- [ ] **Step 5: Commit（logger）**

Run: `git add web/logger.go web/logger_test.go; git commit -m "feat(web): 按天 jsonl 调用日志 logger"`
Expected: 提交成功。

**（本任务第二部分：网关 gateway.go）**

- [ ] **Step 6: 写失败测试（gateway）**

`web/gateway_test.go`：
```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// upstream returns chat completions, optionally as SSE.
type upstreamHandler struct {
	stream bool
	usage  string
}

func (u *upstreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "glm-4"}}})
		return
	}
	if u.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{%s}}`, u.usage)
}

func newGatewayApp(t *testing.T, stream bool, usage string) (*App, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(&upstreamHandler{stream: stream, usage: usage})
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg:      cfg,
		Store:    store,
		Rl:       NewRateLimiter(cfg.RateLimitPerMin),
		Sessions: NewSessionStore(),
		Logger:   NewLogger(t.TempDir()),
		Client:   up.Client(),
	}
	return app, up
}

func mustCreateKey(t *testing.T, app *App, model string) *Key {
	t.Helper()
	k, err := app.Store.CreateKey(model, app.Cfg.TTLDays, app.Cfg.TokenQuota, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func callV1(t *testing.T, app *App, key string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.HandleV1(rec, req)
	return rec
}

func TestV1AuthRejections(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`)

	if rec := callV1(t, app, "", "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key code = %d, want 401", rec.Code)
	}
	if rec := callV1(t, app, "sk-nosuch", "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key code = %d, want 401", rec.Code)
	}

	k := mustCreateKey(t, app, "glm-4")
	if err := app.Store.SetDisabled(k.ID, true); err != nil {
		t.Fatal(err)
	}
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled code = %d, want 403", rec.Code)
	}
	if err := app.Store.SetDisabled(k.ID, false); err != nil {
		t.Fatal(err)
	}

	// wrong model
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"kimi-k2"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong model code = %d, want 403", rec.Code)
	}
}

func TestV1QuotaExhausted(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`)
	k, err := app.Store.CreateKey("glm-4", 7, 100, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	app.Store.AddUsage(k.ID, 100) // 用满额度
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quota code = %d, want 429", rec.Code)
	}
}

func TestV1UpstreamSuccessAndUsage(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":10,"completion_tokens":20,"total_tokens":30`)
	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got, _ := app.Store.GetByID(k.ID); got.TokenUsed != 30 {
		t.Fatalf("TokenUsed = %d, want 30", got.TokenUsed)
	}
}

func TestV1StreamUsage(t *testing.T) {
	app, _ := newGatewayApp(t, true, "")
	k := mustCreateKey(t, app, "glm-4")
	rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4","stream":true}`)
	if !strings.Contains(rec.Body.String(), `[DONE]`) {
		t.Fatalf("stream body = %s", rec.Body.String())
	}
	if got, _ := app.Store.GetByID(k.ID); got.TokenUsed != 30 {
		t.Fatalf("stream TokenUsed = %d, want 30", got.TokenUsed)
	}
}

func TestV1RateLimit(t *testing.T) {
	app, _ := newGatewayApp(t, false, `"prompt_tokens":1,"completion_tokens":1,"total_tokens":2`)
	app.Rl = NewRateLimiter(1)
	k := mustCreateKey(t, app, "glm-4")
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusOK {
		t.Fatalf("first code = %d", rec.Code)
	}
	if rec := callV1(t, app, k.Key, "/v1/chat/completions", `{"model":"glm-4"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second code = %d, want 429", rec.Code)
	}
}
```

- [ ] **Step 7: 跑 gateway 测试确认失败**

Run: `go test ./web/ -run TestV1 -v`
Expected: FAIL（`HandleV1` 未定义）。

- [ ] **Step 8: 实现 gateway.go**

```go
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(h)
}

type v1Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (a *App) HandleV1(w http.ResponseWriter, r *http.Request) {
	key := bearerToken(r)
	if key == "" {
		writeErr(w, http.StatusUnauthorized, "missing api key")
		return
	}
	k, ok := a.Store.GetByKey(key)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	now := time.Now()
	if k.Disabled {
		writeErr(w, http.StatusForbidden, "key disabled")
		return
	}
	if now.After(k.ExpiresAt) {
		writeErr(w, http.StatusForbidden, "key expired")
		return
	}
	if k.TokenUsed >= k.Quota {
		writeErr(w, http.StatusTooManyRequests, "token quota exhausted")
		return
	}

	// 读取 body 用于模型匹配
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot read body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(raw, &payload)
	if payload.Model != "" && payload.Model != k.Model {
		writeErr(w, http.StatusForbidden, "model not allowed for this key")
		return
	}
	if !a.Rl.Allow(k.ID, now) {
		writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// 构造上游 URL：UpstreamBaseURL 形如 http://host/v1，请求路径 /v1/xx
	target := a.Cfg.UpstreamBaseURL + strings.TrimPrefix(r.URL.Path, "/v1")
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(raw))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "upstream request failed")
		return
	}
	outReq.Header = r.Header.Clone()

	start := time.Now()
	resp, err := a.Client.Do(outReq)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	prompt, completion := a.drainUpstream(w, resp)

	app.Store.AddUsage(k.ID, prompt+completion)
	_ = a.Logger.Write(LogEntry{
		Time:             time.Now().Format(time.RFC3339),
		KeyID:            k.ID,
		Model:            k.Model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		Total:            prompt + completion,
		Status:           resp.StatusCode,
		Ms:               ms,
	})
}

// drainUpstream 把上游响应写回客户端，并尽最大努力解析 usage，返回 prompt/completion token 数。
func (a *App) drainUpstream(w http.ResponseWriter, resp *http.Response) (prompt, completion int) {
	isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	// 先复制响应头
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if isStream {
		return a.drainSSE(w, resp.Body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0
	}
	_, _ = w.Write(body)
	var u v1Usage
	if jsonErr := json.Unmarshal(body, &u); jsonErr == nil {
		return u.PromptTokens, u.CompletionTokens
	}
	// usage 可能在顶层对象里
	var obj map[string]json.RawMessage
	if jsonErr := json.Unmarshal(body, &obj); jsonErr == nil {
		if use, ok := obj["usage"]; ok {
			var uu v1Usage
			if jsonErr := json.Unmarshal(use, &uu); jsonErr == nil {
				return uu.PromptTokens, uu.CompletionTokens
			}
		}
	}
	return 0, 0
}

func (a *App) drainSSE(w http.ResponseWriter, body io.Reader) (prompt, completion int) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		_, _ = w.Write([]byte(line + "\n"))
		if w2, ok := w.(http.Flusher); ok {
			w2.Flush()
		}
		if strings.HasPrefix(line, "data: ") {
			content := strings.TrimPrefix(line, "data: ")
			if strings.TrimSpace(content) == "[DONE]" {
				continue
			}
			var u v1Usage
			if jsonErr := json.Unmarshal([]byte(content), &u); jsonErr == nil && u.TotalTokens > 0 {
				prompt = u.PromptTokens
				completion = u.CompletionTokens
			}
		}
	}
	return prompt, completion
}
```

- [ ] **Step 9: 跑 gateway 测试确认通过**

Run: `go test ./web/ -run TestV1 -v`
Expected: PASS。

- [ ] **Step 10: Commit**

Run: `git add web/gateway.go web/gateway_test.go; git commit -m "feat(web): /v1 网关校验与转发 gateway"`
Expected: 提交成功。

---

### Task 8: 管理后台接口 admin.go

**Files:**
- Create: `web/admin.go`
- Test: `web/admin_test.go`

**Interfaces:**
- Consumes: `Config`、`Store`、`SessionStore`、`Logger`、`FetchModels`、`App`。
- Produces（`App` 方法）:
  - `func (a *App) handleLogin(w http.ResponseWriter, r *http.Request)`：`POST /api/admin/login`，body `{"password":"..."}`；`VerifyPassword(a.Cfg.AdminPasswordHash, password)` 通过 → 200 `{"token":"..."}`；否则 401。
  - `func (a *App) adminAuth(next http.Handler) http.Handler`：校验 `Authorization: Bearer <token>` 在 `Sessions` 有效；无效 401。
  - `func (a *App) handleOverview(...)`：返回 `{"total_keys":N,"active_keys":N,"requests_today":N,"tokens_today":N}`（`active_keys`=未禁用且未过期；今日统计读 `Logger.ReadLogs(今日,"")`）。
  - `func (a *App) handleAdminModels(...)`：`GET`，`FetchModels(a.Cfg.UpstreamBaseURL, a.Client)` → `{"models":[...],"allowed":[...]}`；上游不可达时 `models` 返回空数组并带 `"warning":"..."`。
  - `func (a *App) handleSaveModels(...)`：`POST`，body `{"allowed_models":["..."]}` → 写 `a.Cfg.AllowedModels` 并 `Save`。
  - `func (a *App) handleGetSettings(...)` / `handlePutSettings(...)`：`GET`/`PUT /api/admin/settings`；PUT body `{"upstream_base_url","ttl_days","token_quota","rate_limit_per_min"}`，校验正整数，更新 cfg + 重建 `Rl` + `Save`。
  - `func (a *App) handlePutPassword(...)`：`PUT /api/admin/password`，body `{"password":"..."}`，至少 4 位，更新 hash + `Save`。
  - `func (a *App) handleListKeys(...)`：`GET /api/admin/keys` → `{"keys":[{id,key,model,created_at,expires_at,token_used,quota,disabled,remaining}]}`（`remaining = quota - token_used`）。
  - `func (a *App) handleDisableKey / handleEnableKey(...)`：`POST /api/admin/keys/{id}/disable|enable`，`Store.SetDisabled`。
  - `func (a *App) handleDeleteKey(...)`：`DELETE /api/admin/keys/{id}`。
  - `func (a *App) handleLogs(...)`：`GET /api/admin/logs?key_id=&date=`，默认 `date=今日` → `{"logs":[...]}`。

- [ ] **Step 1: 写失败测试**

`web/admin_test.go`：
```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAdminApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("admin123")
	cfg.UpstreamBaseURL = "http://127.0.0.1:1/v1" // 不可达，仅测试 warning 分支
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Cfg:        cfg,
		Store:      store,
		Rl:         NewRateLimiter(cfg.RateLimitPerMin),
		Sessions:   NewSessionStore(),
		Logger:     NewLogger(t.TempDir()),
		Client:     http.DefaultClient,
		ConfigPath: t.TempDir(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/login", app.handleLogin)
	mux.Handle("GET /api/admin/overview", app.adminAuth(http.HandlerFunc(app.handleOverview)))
	mux.Handle("GET /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handleGetSettings)))
	mux.Handle("PUT /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handlePutSettings)))
	mux.Handle("GET /api/admin/keys", app.adminAuth(http.HandlerFunc(app.handleListKeys)))
	mux.Handle("POST /api/admin/keys/{id}/disable", app.adminAuth(http.HandlerFunc(app.handleDisableKey)))
	mux.Handle("POST /api/admin/keys/{id}/enable", app.adminAuth(http.HandlerFunc(app.handleEnableKey)))
	mux.Handle("DELETE /api/admin/keys/{id}", app.adminAuth(http.HandlerFunc(app.handleDeleteKey)))
	mux.Handle("PUT /api/admin/password", app.adminAuth(http.HandlerFunc(app.handlePutPassword)))
	mux.Handle("GET /api/admin/logs", app.adminAuth(http.HandlerFunc(app.handleLogs)))
	return app, httptest.NewServer(mux)
}

func adminToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/api/admin/login", "application/json",
		bytes.NewBufferString(`{"password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login code = %d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Token
}

func TestAdminLogin(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	if adminToken(t, srv) == "" {
		t.Fatal("empty token")
	}
	resp, _ := srv.Client().Post(srv.URL+"/api/admin/login", "application/json",
		bytes.NewBufferString(`{"password":"wrong"}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password code = %d, want 401", resp.StatusCode)
	}
}

func TestAdminRequiresToken(t *testing.T) {
	_, srv := newAdminApp(t)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/admin/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", resp.StatusCode)
	}
}

func TestAdminSettingsRoundTrip(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	body := `{"upstream_base_url":"http://1.2.3.4:18080/v1","ttl_days":3,"token_quota":500000,"rate_limit_per_min":10}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings code = %d", resp.StatusCode)
	}
	if app.Cfg.TTLDays != 3 || app.Cfg.TokenQuota != 500000 || app.Cfg.RateLimitPerMin != 10 {
		t.Fatalf("cfg not updated: %+v", app.Cfg)
	}
	// 无效值应被拒
	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/settings",
		bytes.NewBufferString(`{"ttl_days":0,"token_quota":-1,"rate_limit_per_min":0}`))
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, _ := srv.Client().Do(req2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid settings code = %d, want 400", resp2.StatusCode)
	}
}

func TestAdminPasswordChange(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/admin/password",
		bytes.NewBufferString(`{"password":"newpass99"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d", resp.StatusCode)
	}
	if !VerifyPassword(app.Cfg.AdminPasswordHash, "newpass99") {
		t.Fatal("password not updated")
	}
}

func TestAdminKeyLifecycle(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	k, _ := app.Store.CreateKey("glm-4", 7, 1000, time.Now())

	do := func(method, path, body string) *http.Response {
		var rdr *bytes.Reader
		if body == "" {
			rdr = bytes.NewReader(nil)
		} else {
			rdr = bytes.NewReader([]byte(body))
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := do(http.MethodGet, "/api/admin/keys", "")
	var list struct {
		Keys []map[string]any `json:"keys"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Keys) != 1 {
		t.Fatalf("keys len = %d", len(list.Keys))
	}

	resp = do(http.MethodPost, "/api/admin/keys/"+k.ID+"/disable", "")
	resp.Body.Close()
	if got, _ := app.Store.GetByID(k.ID); !got.Disabled {
		t.Fatal("expected disabled")
	}
	resp = do(http.MethodPost, "/api/admin/keys/"+k.ID+"/enable", "")
	resp.Body.Close()
	if got, _ := app.Store.GetByID(k.ID); got.Disabled {
		t.Fatal("expected enabled")
	}
	resp = do(http.MethodDelete, "/api/admin/keys/"+k.ID, "")
	resp.Body.Close()
	if _, ok := app.Store.GetByID(k.ID); ok {
		t.Fatal("expected deleted")
	}
}

func TestAdminLogsEndpoint(t *testing.T) {
	app, srv := newAdminApp(t)
	defer srv.Close()
	tok := adminToken(t, srv)

	_ = app.Logger.Write(LogEntry{Time: time.Now().Format(time.RFC3339), KeyID: "k1", Model: "glm-4", Total: 10, Status: 200, Ms: 1})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/admin/logs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Logs []LogEntry `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Logs) != 1 || body.Logs[0].KeyID != "k1" {
		t.Fatalf("logs = %+v", body.Logs)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./web/ -run TestAdmin -v`
Expected: FAIL。

- [ ] **Step 3: 实现 admin.go**

```go
package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !VerifyPassword(a.Cfg.AdminPasswordHash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "bad password")
		return
	}
	tok := a.Sessions.Create(12 * time.Hour)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (a *App) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Sessions.Valid(bearerToken(r), time.Now()) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	keys := a.Store.List()
	active := 0
	now := time.Now()
	for _, k := range keys {
		if !k.Disabled && now.Before(k.ExpiresAt) {
			active++
		}
	}
	reqTotal, tokTotal := 0, 0
	if logs, err := a.Logger.ReadLogs(time.Now().Format("2006-01-02"), ""); err == nil {
		for _, e := range logs {
			reqTotal++
			tokTotal += e.Total
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_keys":    len(keys),
		"active_keys":   active,
		"requests_today": reqTotal,
		"tokens_today":  tokTotal,
	})
}

func (a *App) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"models": []string{}, "allowed": a.Cfg.AllowedModels}
	models, err := FetchModels(a.Cfg.UpstreamBaseURL, a.Client)
	if err != nil {
		out["warning"] = "无法连接上游拉取模型: " + err.Error()
	} else {
		out["models"] = models
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleSaveModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowedModels []string `json:"allowed_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	a.Cfg.AllowedModels = req.AllowedModels
	if err := a.Cfg.Save(a.ConfigPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"upstream_base_url":  a.Cfg.UpstreamBaseURL,
		"ttl_days":           a.Cfg.TTLDays,
		"token_quota":        a.Cfg.TokenQuota,
		"rate_limit_per_min": a.Cfg.RateLimitPerMin,
	})
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UpstreamBaseURL  string `json:"upstream_base_url"`
		TTLDays          int    `json:"ttl_days"`
		TokenQuota       int    `json:"token_quota"`
		RateLimitPerMin  int    `json:"rate_limit_per_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.TTLDays <= 0 || req.TokenQuota <= 0 || req.RateLimitPerMin < 0 {
		writeErr(w, http.StatusBadRequest, "invalid values")
		return
	}
	a.Cfg.UpstreamBaseURL = req.UpstreamBaseURL
	a.Cfg.TTLDays = req.TTLDays
	a.Cfg.TokenQuota = req.TokenQuota
	a.Cfg.RateLimitPerMin = req.RateLimitPerMin
	a.Rl = NewRateLimiter(a.Cfg.RateLimitPerMin)
	if err := a.Cfg.Save(a.ConfigPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handlePutPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.Password) < 4 {
		writeErr(w, http.StatusBadRequest, "password too short")
		return
	}
	a.Cfg.AdminPasswordHash = HashPassword(req.Password)
	if err := a.Cfg.Save(a.ConfigPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys := a.Store.List()
	type outKey struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		Model     string `json:"model"`
		CreatedAt string `json:"created_at"`
		ExpiresAt string `json:"expires_at"`
		TokenUsed int    `json:"token_used"`
		Quota     int    `json:"quota"`
		Remaining int    `json:"remaining"`
		Disabled  bool   `json:"disabled"`
	}
	out := make([]outKey, 0, len(keys))
	for _, k := range keys {
		rem := k.Quota - k.TokenUsed
		if rem < 0 {
			rem = 0
		}
		out = append(out, outKey{
			ID: k.ID, Key: k.Key, Model: k.Model,
			CreatedAt: k.CreatedAt.Format(time.RFC3339),
			ExpiresAt: k.ExpiresAt.Format(time.RFC3339),
			TokenUsed: k.TokenUsed, Quota: k.Quota, Remaining: rem, Disabled: k.Disabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (a *App) handleSetKeyDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	id := r.PathValue("id")
	if err := a.Store.SetDisabled(id, disabled); err != nil {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDisableKey(w http.ResponseWriter, r *http.Request) {
	a.handleSetKeyDisabled(w, r, true)
}

func (a *App) handleEnableKey(w http.ResponseWriter, r *http.Request) {
	a.handleSetKeyDisabled(w, r, false)
}

func (a *App) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.Delete(id); err != nil {
		writeErr(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	keyID := r.URL.Query().Get("key_id")
	logs, err := a.Logger.ReadLogs(date, keyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read logs failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./web/ -run TestAdmin -v`
Expected: PASS。

- [ ] **Step 5: Commit**

Run: `git add web/admin.go web/admin_test.go; git commit -m "feat(web): 管理后台接口 admin"`
Expected: 提交成功。

---

### Task 9: 前端——领 Key 页 static（index.html + app.js + style.css）

**Files:**
- Create: `web/static/index.html`、`web/static/app.js`、`web/static/style.css`

**Interfaces:**
- Consumes: `GET /api/models`、`POST /api/keys`（Task 6）。
- Produces: 内嵌前端资源（`go:embed static` 由 Task 11 装配）。`index.html` 引用 `style.css` 与 `app.js`。

- [ ] **Step 1: 创建 style.css**

`web/static/style.css`：
```css
:root { --bg:#f4f4f5; --card:#fff; --accent:#0f766e; --muted:#6b7280; --err:#b91c1c; }
* { box-sizing:border-box; margin:0; padding:0; }
body { font:16px/1.6 -apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif; background:var(--bg); color:#1f2937; }
header { display:flex; justify-content:space-between; align-items:center; padding:16px 28px; background:var(--card); border-bottom:1px solid #e5e7eb; }
.brand { font-weight:700; font-size:18px; }
.link-btn { background:none; border:1px solid #d1d5db; border-radius:8px; padding:6px 12px; cursor:pointer; color:#374151; }
.link-btn:hover { border-color:var(--accent); color:var(--accent); }
main.card { max-width:560px; margin:48px auto; background:var(--card); border-radius:16px; padding:32px; box-shadow:0 1px 3px rgba(0,0,0,.06); }
h1 { font-size:22px; margin-bottom:6px; }
.hint { color:var(--muted); margin-bottom:22px; }
label { display:block; font-weight:600; margin-bottom:6px; }
select, .primary { width:100%; padding:10px 12px; font-size:15px; border-radius:10px; border:1px solid #d1d5db; margin-bottom:14px; }
.primary { background:var(--accent); color:#fff; border:none; cursor:pointer; font-weight:600; }
.primary:disabled { opacity:.6; cursor:not-allowed; }
.result { background:#f9fafb; border:1px solid #e5e7eb; border-radius:12px; padding:16px; margin-top:6px; }
.result > div { margin-bottom:10px; }
.k { display:block; font-size:12px; color:var(--muted); margin-bottom:2px; }
code { display:block; word-break:break-all; background:#f3f4f6; padding:8px; border-radius:8px; font-size:14px; }
.tiny { color:var(--muted); font-size:13px; margin-top:12px; }
.error { color:var(--err); margin-top:12px; font-weight:600; }
.hidden { display:none; }
#copyBtn { margin-top:8px; padding:8px 14px; border-radius:8px; border:1px solid var(--accent); background:#fff; color:var(--accent); cursor:pointer; font-weight:600; }
table { width:100%; border-collapse:collapse; font-size:14px; }
th, td { text-align:left; padding:8px 10px; border-bottom:1px solid #eef2f2; vertical-align:middle; }
th { color:var(--muted); font-weight:600; }
button.small { padding:4px 10px; border-radius:6px; border:1px solid #d1d5db; background:#fff; cursor:pointer; font-size:13px; margin-right:4px; }
button.small.danger { color:var(--err); border-color:#fca5a5; }
```

- [ ] **Step 2: 创建 index.html**

`web/static/index.html`：
```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>领取 API Key</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<header>
  <div class="brand">opencode2api 自助领 Key</div>
  <button id="adminLink" class="link-btn">⚙ 管理员入口</button>
</header>
<main class="card">
  <h1>领取 API Key</h1>
  <p class="hint">选择一个模型，点击获取，即可拿到你的访问地址和 Key。</p>
  <label for="model">选择模型</label>
  <select id="model"></select>
  <button id="getKeyBtn" class="primary">获取 Key</button>
  <div id="result" class="result hidden">
    <div><span class="k">访问地址</span><code id="baseUrl"></code></div>
    <div><span class="k">你的 Key</span><code id="apiKey"></code></div>
    <button id="copyBtn">一键复制</button>
    <p class="tiny">把上面的地址和 Key 填到 OpenCode / Cherry Studio / Cursor 等客户端的自定义模型配置里即可使用。</p>
  </div>
  <div id="error" class="error hidden"></div>
</main>
<script src="app.js"></script>
</body>
</html>
```

- [ ] **Step 3: 创建 app.js**

`web/static/app.js`：
```js
async function loadModels() {
  try {
    const res = await fetch('/api/models');
    const data = await res.json();
    const sel = document.getElementById('model');
    sel.innerHTML = '';
    if (!data.models.length) {
      const o = document.createElement('option');
      o.textContent = '（暂无可领取的模型）';
      sel.appendChild(o);
      document.getElementById('getKeyBtn').disabled = true;
      return;
    }
    data.models.forEach(m => {
      const o = document.createElement('option');
      o.value = m; o.textContent = m;
      sel.appendChild(o);
    });
  } catch (e) {
    const err = document.getElementById('error');
    err.textContent = '加载模型失败：' + e; err.classList.remove('hidden');
  }
}

async function getKey() {
  const model = document.getElementById('model').value;
  const err = document.getElementById('error');
  err.classList.add('hidden');
  try {
    const res = await fetch('/api/keys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model })
    });
    const data = await res.json();
    if (!res.ok) {
      err.textContent = data.error || '领取失败'; err.classList.remove('hidden');
      return;
    }
    document.getElementById('baseUrl').textContent = data.base_url;
    document.getElementById('apiKey').textContent = data.key;
    document.getElementById('result').classList.remove('hidden');
  } catch (e) {
    err.textContent = '请求失败：' + e; err.classList.remove('hidden');
  }
}

document.getElementById('getKeyBtn').addEventListener('click', getKey);
document.getElementById('copyBtn').addEventListener('click', async () => {
  const text = document.getElementById('baseUrl').textContent + '\n' + document.getElementById('apiKey').textContent;
  try { await navigator.clipboard.writeText(text); } catch (e) { /* 简版：不回退 */ }
  document.getElementById('copyBtn').textContent = '已复制 ✓';
  setTimeout(() => { document.getElementById('copyBtn').textContent = '一键复制'; }, 1500);
});
document.getElementById('adminLink').addEventListener('click', () => { location.href = '/admin.html'; });

loadModels();
```

- [ ] **Step 4: Commit**

Run: `git add web/static/index.html web/static/app.js web/static/style.css; git commit -m "feat(web): 领 Key 前端页面 static"`
Expected: 提交成功。

---

### Task 10: 前端——管理后台 static（admin.html + admin.js）

**Files:**
- Create: `web/static/admin.html`、`web/static/admin.js`

**Interfaces:**
- Consumes: `POST /api/admin/login`、及 `GET/POST /api/admin/*`（Task 8）。
- Produces: 管理后台单页，含登录弹窗与 5 个导航视图（概览 / 模型 / 设置 / Key / 日志）。登录令牌存 `localStorage`，请求携带 `Authorization: Bearer <token>`。

- [ ] **Step 1: 创建 admin.html**

`web/static/admin.html`：
```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>管理后台</title>
<link rel="stylesheet" href="style.css">
<style>
.layout { display:flex; max-width:1100px; margin:24px auto; gap:20px; padding:0 16px; }
nav { width:200px; flex-shrink:0; background:var(--card); border-radius:14px; padding:14px; height:fit-content; }
nav button { display:block; width:100%; text-align:left; padding:10px 12px; border:none; background:none; cursor:pointer; border-radius:8px; font-size:15px; color:#374151; }
nav button:hover { background:#f3f4f6; }
nav button.active { background:var(--accent); color:#fff; font-weight:600; }
.content { flex:1; background:var(--card); border-radius:14px; padding:24px; min-height:400px; }
.view { display:none; } .view.active { display:block; }
h2 { margin-bottom:8px; }
.clash { display:flex; gap:6px; align-items:center; color:var(--err); font-size:14px; }
.badge { display:inline-block; background:#f3f4f6; border-radius:10px; padding:2px 8px; font-size:13px; }
.label { font-size:13px; color:var(--muted); margin:12px 0 4px; }
input { width:100%; padding:9px 12px; border:1px solid #d1d5db; border-radius:10px; font-size:14px; margin-bottom:10px; }
</style>
</head>
<body>
<header>
  <div class="brand">管理后台</div>
  <button id="logout" class="link-btn hidden">退出</button>
</header>

<div id="loginMask" class="login-mask"></div>

<main class="layout hidden" id="main">
  <nav>
    <button data-view="overview" class="active">概览</button>
    <button data-view="models">模型管理</button>
    <button data-view="settings">参数设置</button>
    <button data-view="keys">Key 管理</button>
    <button data-view="logs">调用日志</button>
  </nav>
  <section class="content">
    <div id="overview" class="view active"></div>
    <div id="models" class="view"></div>
    <div id="settings" class="view"></div>
    <div id="keys" class="view"></div>
    <div id="logs" class="view"></div>
  </section>
</main>
<script src="admin.js"></script>
</body>
</html>
```

- [ ] **Step 2: 创建 admin.js**

`web/static/admin.js`：
```js
let token = localStorage.getItem('token') || '';

async function api(path, method = 'GET', body) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = 'Bearer ' + token;
  const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const data = await res.json().catch(() => ({}));
  return { ok: res.ok, status: res.status, data };
}

function el(id) { return document.getElementById(id); }
function fmtHT(s) { return new Date(s).toLocaleString(); }

async function login(pw) {
  const r = await fetch('/api/admin/login', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: pw })
  });
  const d = await r.json();
  if (!r.ok) return false;
  token = d.token; localStorage.setItem('token', token);
  return true;
}

function logout() { token = ''; localStorage.removeItem('token'); showLogin(); }

function showLogin() {
  const pw = prompt('请输入管理员密码');
  if (pw === null) return;
  login(pw).then(ok => { if (ok) enter(); else { alert('密码错误'); showLogin(); } });
}

function enter() {
  el('loginMask').style.display = 'none';
  el('main').classList.remove('hidden');
  el('logout').classList.remove('hidden');
  switchView('overview');
}

async function switchView(name) {
  document.querySelectorAll('nav button').forEach(b => b.classList.toggle('active', b.dataset.view === name));
  const views = { overview: loadOverview, models: loadModels, settings: loadSettings, keys: loadKeys, logs: loadLogs };
  if (views[name]) views[name]();
  document.querySelectorAll('.view').forEach(v => v.classList.toggle('active', v.id === name));
}

// ---- 概览 ----
async function loadOverview() {
  const { ok, data } = await api('/api/admin/overview');
  if (!ok) return onAuthFail(data);
  el('overview').innerHTML = `
    <h2>概览</h2>
    <div class="label">Key 总数：<span class="badge">${data.total_keys}</span></div>
    <div class="label">在用 Key：<span class="badge">${data.active_keys}</span></div>
    <div class="label">今日请求：<span class="badge">${data.requests_today}</span></div>
    <div class="label">今日 token：<span class="badge">${data.tokens_today}</span></div>`;
}

// ---- 模型管理 ----
async function loadModels() {
  const { ok, data } = await api('/api/admin/models');
  if (!ok) return onAuthFail(data);
  let html = '<h2>模型管理</h2>';
  if (data.warning) html += `<div class="clash">⚠ ${data.warning}</div>`;
  if (!data.models.length) html += '<p class="hint">上游暂无可用模型。</p>';
  data.models.forEach(m => {
    const checked = (data.allowed || []).includes(m) ? 'checked' : '';
    html += `<label style="display:flex;gap:8px;align-items:center;margin:6px 0;"><input type="checkbox" class="model-cb" value="${m}" ${checked}> ${m}</label>`;
  });
  html += '<button class="primary" style="margin-top:12px;" onclick="saveModels()">保存模型白名单</button>';
  el('models').innerHTML = html;
}
async function saveModels() {
  const allowed = [...document.querySelectorAll('.model-cb:checked')].map(b => b.value);
  const { ok, data } = await api('/api/admin/models', 'POST', { allowed_models: allowed });
  if (!ok) return onAuthFail(data);
  alert('已保存');
}

// ---- 参数设置 ----
async function loadSettings() {
  const { ok, data } = await api('/api/admin/settings');
  if (!ok) return onAuthFail(data);
  el('settings').innerHTML = `
    <h2>参数设置</h2>
    <div class="label">上游实例地址（http://host/v1）</div>
    <input id="upstream" value="${data.upstream_base_url || ''}" placeholder="http://127.0.0.1:18080/v1">
    <div class="label">有效期（天）</div><input id="ttl" type="number" value="${data.ttl_days}">
    <div class="label">总 token 额度</div><input id="quota" type="number" value="${data.token_quota}">
    <div class="label">限速（每分钟请求数）</div><input id="rate" type="number" value="${data.rate_limit_per_min}">
    <button class="primary" onclick="saveSettings()">保存参数</button>
    <h2 style="margin-top:24px;">修改管理员密码</h2>
    <input id="newpw" type="password" placeholder="新密码（至少 4 位）">
    <button class="primary" onclick="savePassword()">修改密码</button>`;
}
async function saveSettings() {
  const src = el('upstream').value.trim();
  const { ok, data } = await api('/api/admin/settings', 'PUT', {
    upstream_base_url: src,
    ttl_days: parseInt(el('ttl').value),
    token_quota: parseInt(el('quota').value),
    rate_limit_per_min: parseInt(el('rate').value)
  });
  if (!ok) return onAuthFail(data, () => el('settings').scrollIntoView());
  alert('已保存');
}
async function savePassword() {
  const pw = el('newpw').value;
  if (pw.length < 4) return alert('密码至少 4 位');
  const { ok, data } = await api('/api/admin/password', 'PUT', { password: pw });
  if (!ok) return onAuthFail(data);
  alert('密码已修改');
}

// ---- Key 管理 ----
async function loadKeys() {
  const { ok, data } = await api('/api/admin/keys');
  if (!ok) return onAuthFail(data);
  if (!data.keys.length) { el('keys').innerHTML = '<h2>Key 管理</h2><p class="hint">还没有 Key。</p>'; return; }
  let rows = data.keys.map(k => `
    <tr>
      <td>${k.id.slice(0, 8)}…</td>
      <td><code>${k.key}</code></td>
      <td>${k.model}</td>
      <td>${fmtHT(k.created_at)}</td>
      <td>${fmtHT(k.expires_at)}</td>
      <td>${k.token_used}/${k.quota}</td>
      <td>${k.remaining}</td>
      <td>${k.disabled ? '已禁用' : '正常'}</td>
      <td>
        <button class="small" onclick="toggleKey('${k.id}', ${!k.disabled})">${k.disabled ? '启用' : '禁用'}</button>
        <button class="small danger" onclick="delKey('${k.id}')">删除</button>
      </td>
    </tr>`).join('');
  el('keys').innerHTML = `<h2>Key 管理</h2>
    <table><thead><tr><th>ID</th><th>Key</th><th>模型</th><th>领取时间</th><th>过期时间</th><th>已用/额度</th><th>剩余</th><th>状态</th><th>操作</th></tr></thead>
    <tbody>${rows}</tbody></table>`;
}
async function toggleKey(id, disable) {
  const { ok, data } = await api(`/api/admin/keys/${id}/${disable ? 'disable' : 'enable'}`, 'POST', {});
  if (!ok) return onAuthFail(data);
  loadKeys();
}
async function delKey(id) {
  if (!confirm('确定删除该 Key？')) return;
  const { ok, data } = await api('/api/admin/keys/' + id, 'DELETE');
  if (!ok) return onAuthFail(data);
  loadKeys();
}

// ---- 日志 ----
async function loadLogs() {
  const { ok, data } = await api('/api/admin/logs');
  if (!ok) return onAuthFail(data);
  if (!data.logs.length) { el('logs').innerHTML = '<h2>调用日志</h2><p class="hint">今日暂无记录。</p>'; return; }
  let rows = data.logs.map(e => `<tr><td>${fmtHT(e.time)}</td><td>${e.key_id.slice(0, 8)}…</td><td>${e.model}</td><td>${e.prompt_tokens}</td><td>${e.completion_tokens}</td><td>${e.total}</td><td>${e.status}</td><td>${e.ms}ms</td></tr>`).join('');
  el('logs').innerHTML = `<h2>调用日志（今日）</h2>
    <table><thead><tr><th>时间</th><th>Key</th><th>模型</th><th>输入</th><th>输出</th><th>总token</th><th>状态</th><th>耗时</th></tr></thead>
    <tbody>${rows}</tbody></table>`;
}

function onAuthFail(data) {
  if (data.error === 'unauthorized') { logout(); }
  else alert(data.error || '请求失败');
}

document.querySelectorAll('nav button').forEach(b => b.addEventListener('click', () => switchView(b.dataset.view)));
el('logout').addEventListener('click', logout);

(async function init() {
  if (token) {
    const r = await api('/api/admin/overview');
    if (r.ok) { enter(); return; }
  }
  showLogin();
})();
```

- [ ] **Step 3: 补充 admin.html 登录遮罩样式**

在 `admin.html` 的 `<style>` 内追加：
```css
.login-mask { position:fixed; inset:0; background:var(--bg); z-index:10; }
```
（`showLogin()` 用 `prompt()`，遮罩仅统一视觉，逻辑由 `admin.js` 控制。）

- [ ] **Step 4: Commit**

Run: `git add web/static/admin.html web/static/admin.js; git commit -m "feat(web): 管理后台前端页面 static"`
Expected: 提交成功。

---

### Task 11: 程序入口 + 冒烟测试 + 验收 main.go / main_test.go

**Files:**
- Create: `web/main.go`、`web/main_test.go`
- Modify: `.gitignore`
- Docs: `docs/opencode2api-web-self-service.md`（运行说明）

**Interfaces:**
- Consumes: 前面全部任务；`static/` 需在包目录内以便 `go:embed`。
- Produces:
  - `func buildMux(app *App) http.Handler`：装配全部路由（静态 `/`、`/api/*`、`/api/admin/*`、`/v1/`、`/admin.html`）。可被测试直接调用。
  - `main()`：解析 `-data-dir`、`LoadConfig`、首次运行生成随机管理员密码并打印、`NewStore`、组装 `App`、启动 HTTP。

- [ ] **Step 1: 更新 .gitignore**

在 `.gitignore` 的 Build output 段落追加：
```
# Web self-service runtime data
web-data/
```

- [ ] **Step 2: 写冒烟测试**

`web/main_test.go`：
```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newFullApp(t *testing.T) *App {
	t.Helper()
	up := httptest.NewServer(&upstreamHandler{stream: false, usage: `"prompt_tokens":10,"completion_tokens":20,"total_tokens":30`})
	t.Cleanup(up.Close)
	cfg := DefaultConfig()
	cfg.AdminPasswordHash = HashPassword("admin123")
	cfg.UpstreamBaseURL = up.URL + "/v1"
	cfg.AllowedModels = []string{"glm-4"}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		Cfg: cfg, Store: store,
		Rl: NewRateLimiter(cfg.RateLimitPerMin), Sessions: NewSessionStore(),
		Logger: NewLogger(t.TempDir()), Client: up.Client(), ConfigPath: t.TempDir(),
	}
}

func TestServeIndexPage(t *testing.T) {
	mux := buildMux(newFullApp(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "领取 API Key") {
		t.Fatalf("index code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServeAdminPage(t *testing.T) {
	mux := buildMux(newFullApp(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "管理后台") {
		t.Fatalf("admin code=%d", rec.Code)
	}
}

func TestEndToEndKeyFlow(t *testing.T) {
	app := newFullApp(t)
	mux := buildMux(app)

	// 选模型
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if !strings.Contains(rec.Body.String(), "glm-4") {
		t.Fatalf("models body=%s", rec.Body.String())
	}

	// 领 Key
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/keys",
		bytes.NewBufferString(`{"model":"glm-4"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create key code=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	// 用 Key 调用网关
	body := `{"model":"glm-4","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+issued.Key)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("v1 code=%d body=%s", rec.Code, rec.Body.String())
	}

	// Key 被禁用后应 403
	id, _ := app.Store.GetByKey(issued.Key)
	_ = app.Store.SetDisabled(id.ID, true)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled code=%d, want 403", rec.Code)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./web/ -run TestServe -v`
Expected: FAIL（`buildMux` 未定义）。

- [ ] **Step 4: 实现 main.go**

`web/main.go`：
```go
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"
)

//go:embed static
var staticFS embed.FS

var version = "dev"

func buildMux(app *App) http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/models", app.HandleModels)
	mux.HandleFunc("POST /api/keys", app.HandleCreateKey)
	mux.HandleFunc("POST /api/admin/login", app.handleLogin)
	mux.Handle("GET /api/admin/overview", app.adminAuth(http.HandlerFunc(app.handleOverview)))
	mux.Handle("GET /api/admin/models", app.adminAuth(http.HandlerFunc(app.handleAdminModels)))
	mux.Handle("POST /api/admin/models", app.adminAuth(http.HandlerFunc(app.handleSaveModels)))
	mux.Handle("GET /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handleGetSettings)))
	mux.Handle("PUT /api/admin/settings", app.adminAuth(http.HandlerFunc(app.handlePutSettings)))
	mux.Handle("PUT /api/admin/password", app.adminAuth(http.HandlerFunc(app.handlePutPassword)))
	mux.Handle("GET /api/admin/keys", app.adminAuth(http.HandlerFunc(app.handleListKeys)))
	mux.Handle("POST /api/admin/keys/{id}/disable", app.adminAuth(http.HandlerFunc(app.handleDisableKey)))
	mux.Handle("POST /api/admin/keys/{id}/enable", app.adminAuth(http.HandlerFunc(app.handleEnableKey)))
	mux.Handle("DELETE /api/admin/keys/{id}", app.adminAuth(http.HandlerFunc(app.handleDeleteKey)))
	mux.Handle("GET /api/admin/logs", app.adminAuth(http.HandlerFunc(app.handleLogs)))
	mux.HandleFunc("/v1/", app.HandleV1)
	return mux
}

func main() {
	dataDir := flag.String("data-dir", "./web-data", "data directory (config.json, keys.json, logs/)")
	flag.Parse()

	cfg, err := LoadConfig(*dataDir)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if cfg.AdminPasswordHash == "" {
		pw := "admin-" + GenerateKey()[3:]
		cfg.AdminPasswordHash = HashPassword(pw)
		if err := cfg.Save(*dataDir); err != nil {
			log.Fatalf("save bootstrap config: %v", err)
		}
		fmt.Printf("[首次启动] 管理员初始密码：%s（请尽快登录后台修改）\n", pw)
	}

	store, err := NewStore(*dataDir)
	if err != nil {
		log.Fatalf("load store: %v", err)
	}

	app := &App{
		Cfg:        cfg,
		Store:      store,
		Rl:         NewRateLimiter(cfg.RateLimitPerMin),
		Sessions:   NewSessionStore(),
		Logger:     NewLogger(*dataDir),
		Client:     &http.Client{Timeout: 300 * time.Second},
		ConfigPath: *dataDir,
	}

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	log.Printf("opencode2api 网页版 %s 启动，监听 %s，数据目录 %s", version, addr, *dataDir)
	log.Printf("浏览器访问: http://127.0.0.1:%d", cfg.Port)
	if err := http.ListenAndServe(addr, buildMux(app)); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 5: 跑冒烟测试确认通过**

Run: `go test ./web/ -run TestServe -v`
Expected: PASS。

- [ ] **Step 6: 跑全部测试**

Run: `go test ./web/...`
Expected: 全部 PASS（config/store/ratelimit/auth/models/api/admin/gateway/main）。

- [ ] **Step 7: 构建**

Run: `go build -o bin/web-server.exe ./web`
Expected: 生成 `bin/web-server.exe`（无报错）。

- [ ] **Step 8: 端到端手动验收**

Run（PowerShell，可后台）: `.\bin\web-server.exe -data-dir .\web-data`
Expected:
1. 控制台打印管理员初始密码（首次）。
2. 浏览器打开 `http://127.0.0.1:18081` 显示「领取 API Key」页，模型下拉框为空（白名单为空）。
3. 点「管理员入口 → 输初始密码」进入后台。
4. 后台「参数设置」填上游实例地址（如 `http://127.0.0.1:18080/v1`）→ 保存。
5. 后台「模型管理」勾选允许分发的模型 → 保存。
6. 领 Key 页刷新后下拉框显示勾选的模型 → 选模型 → 获取 Key → 出现地址+Key → 一键复制。
7. 把地址+Key 填进客户端（OpenCode 等）→ 调用成功。
8. 后台可禁用/删除 Key、查看概览与日志，改动即时生效。

- [ ] **Step 9: 写运行说明**

Create: `docs/opencode2api-web-self-service.md`。内容：
```markdown
# 网页自助领 Key 服务（opencode2api 网页版）

## 构建
go build -o bin/web-server.exe ./web

## 运行
bin/web-server.exe -data-dir ./web-data
首次运行会在控制台打印管理员初始密码，请立即登录后台修改。

## 使用
- 浏览器打开 http://<服务器IP>:18081 领取 Key。
- 右上角「管理员入口」输入密码进入后台。
- 后台需先填上游实例地址，并勾选要分发的模型。
- 用户把「访问地址 + Key」填到自己的客户端（OpenCode / Cherry Studio / Cursor 等）即可调用。

## 数据
数据存在 -data-dir 指定目录：config.json / keys.json / logs/
```

- [ ] **Step 10: Commit**

Run: `git add web/main.go web/main_test.go .gitignore docs/opencode2api-web-self-service.md; git commit -m "feat(web): 程序入口与冒烟测试，运行说明"`
Expected: 提交成功。

---

## 自审说明

- 类型/签名一致性：`App` 结构在 Task 6 定义一次，后续 Task 全部复用同一字段名（`Cfg/Store/Rl/Sessions/Logger/Client/ConfigPath`）；`HashPassword/VerifyPassword/NewStore/CreateKey/SetDisabled/Delete/ReadLogs/FetchModels/bearerToken` 等在各自 Task 定义并被后续 Task 引用，名称一致。
- 路由命名：`HandleV1`、`HandleModels`、`HandleCreateKey`、`handleLogin`、`handleOverview` 等与 Task 8/11 mux 装配一致；`{id}` 路径参数用 `r.PathValue("id")`（Go 1.22）。
- 无占位符：所有任务 Step 均含实际测试与实现代码。
- 验收对应设计文档的端到端清单。