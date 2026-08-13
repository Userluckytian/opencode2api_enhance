// 订阅源管理（T3：订阅从单条 config 升级为多条列表）。
// 订阅源存储 dataDir/subscriptions.json：每条含 URL / 自动拉取间隔（分钟） / 导入目标。
// 目标：solo = 建实例-独享；pool = 建实例-进池；pool-only = 仅导入节点池。
// 兼容迁移：首次读取时若旧 config.subscribe_url 非空，并入列表为第一条。
package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SubscriptionTarget 订阅导入目标。
type SubscriptionTarget string

const (
	TargetSolo     SubscriptionTarget = "solo"      // 建实例-独享
	TargetPool     SubscriptionTarget = "pool"      // 建实例-进池
	TargetPoolOnly SubscriptionTarget = "pool-only" // 仅导入节点池（不进实例）
)

// SubscriptionSource 一条订阅源。
type SubscriptionSource struct {
	URL         string             `json:"url"`
	IntervalMin int                `json:"interval_min"` // <=0 = 不自动拉取
	Target      SubscriptionTarget `json:"target"`
	Group       string             `json:"group,omitempty"` // 订阅来源分组名（由 URL 派生，删除时按此释放实例）
}

// subscriptionsPath 订阅源列表文件路径。
func (m *Manager) subscriptionsPath() string {
	return filepath.Join(m.paths.DataDir, "subscriptions.json")
}

// saveSubscriptions 持久化订阅源列表。
func (m *Manager) saveSubscriptions(list []SubscriptionSource) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化订阅源失败: %v", err)
	}
	return writeFileMkdir(m.subscriptionsPath(), data)
}

// loadSubscriptions 读取订阅源列表；兼容迁移：旧 config 单条并入。
func (m *Manager) loadSubscriptions() []SubscriptionSource {
	data, err := os.ReadFile(m.subscriptionsPath())
	if err == nil {
		var list []SubscriptionSource
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list
		}
	}
	// 迁移：旧 config subscribe_url 未入库前作为第一条（并立即落盘一次）
	cfg := m.loadConfig()
	if cfg.SubscribeURL != "" {
		migrated := []SubscriptionSource{{
			URL:         cfg.SubscribeURL,
			IntervalMin: cfg.SubscribeIntervalMin,
			Target:      TargetSolo,
		}}
		_ = m.saveSubscriptions(migrated)
		return migrated
	}
	return nil
}

// AddSubscription 新增订阅源；重复 URL 报错。
func (m *Manager) AddSubscription(url string, intervalMin int, target SubscriptionTarget) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return errors.New("订阅 URL 不能为空")
	}
	if target == "" {
		target = TargetSolo
	}
	if target != TargetSolo && target != TargetPool && target != TargetPoolOnly {
		return errors.New("订阅目标仅支持 solo / pool / pool-only")
	}
	list := m.loadSubscriptions()
	for _, s := range list {
		if s.URL == url {
			return errors.New("该订阅已存在")
		}
	}
	list = append(list, SubscriptionSource{URL: url, IntervalMin: intervalMin, Target: target})
	return m.saveSubscriptions(list)
}

// RemoveSubscription 删除订阅源（按 URL）；返回是否删到。
func (m *Manager) RemoveSubscription(url string) (bool, error) {
	url = strings.TrimSpace(url)
	list := m.loadSubscriptions()
	filtered := list[:0]
	removed := false
	for _, s := range list {
		if s.URL == url {
			removed = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !removed || len(filtered) == len(list) {
		return false, nil
	}
	if err := m.saveSubscriptions(filtered); err != nil {
		return false, err
	}
	return true, nil
}

// ImportSubscriptionNow 立即执行某条订阅源的导入（按目标）。
func (m *Manager) ImportSubscriptionNow(url string) (int, string, error) {
	url = strings.TrimSpace(url)
	// 目标从源列表取；未登记则按 solo 处理
	target := TargetSolo
	for _, s := range m.loadSubscriptions() {
		if s.URL == url {
			target = s.Target
			break
		}
	}
	switch target {
	case TargetPoolOnly:
		n, err := m.importSubscriptionPool(url)
		return n, "节点池", err
	default:
		join := target == TargetPool
		n, err := m.importSubscription(url, join)
		if join {
			return n, "实例池", err
		}
		return n, "独享", err
	}
}

// RunAllSubscriptionLoop 后台自动拉取循环：每个订阅源按各自间隔独立调度。
// 历史实现每轮拉取全部 interval>0 的源、再按最短间隔休眠，导致长间隔源被超频拉取
// （如 24h 源与 1min 源并存时 24h 源每轮都被重拉）。现改为逐源记录上次拉取时间，
// 到点才拉，休眠至最近一次到期；订阅源变更（增删/改间隔）无需重启。
func (m *Manager) RunAllSubscriptionLoop() {
	go func() {
		lastPull := map[string]time.Time{}
		for {
			list := m.loadSubscriptions()
			now := time.Now()
			next := now.Add(time.Hour) // 兜底：无到期源时 1h 后复查（避免空列表忙转）
			for _, s := range list {
				if s.IntervalMin <= 0 {
					continue
				}
				interval := time.Duration(s.IntervalMin) * time.Minute
				last, ok := lastPull[s.URL]
				if !ok || now.Sub(last) >= interval {
					if n, err := m.importSubscriptionForSource(s); err != nil {
						slog.Warn("订阅自动拉取失败", "url", s.URL, "error", err)
					} else {
						lastPull[s.URL] = time.Now()
						slog.Info("订阅自动拉取完成", "url", s.URL, "imported", n)
					}
				}
				// 该源下次到期时间（首次未拉成功也按现在+间隔计，避免失败后 30s 忙转）
				due := last.Add(interval)
				if !ok {
					due = now.Add(interval)
				}
				if due.Before(next) {
					next = due
				}
			}
			// 清理已删除订阅的记录
			for u := range lastPull {
				found := false
				for _, s := range list {
					if s.URL == u {
						found = true
						break
					}
				}
				if !found {
					delete(lastPull, u)
				}
			}
			wait := time.Until(next)
			if wait < 30*time.Second {
				wait = 30 * time.Second
			}
			if wait > time.Hour {
				wait = time.Hour
			}
			<-time.After(wait)
		}
	}()
}

// importSubscriptionForSource 按订阅源目标导入。
func (m *Manager) importSubscriptionForSource(s SubscriptionSource) (int, error) {
	switch s.Target {
	case TargetPoolOnly:
		return m.importSubscriptionPool(s.URL)
	default:
		return m.importSubscription(s.URL, s.Target == TargetPool)
	}
}

// ---------- HTTP handlers ----------

// SubscriptionsListHandler GET → 全部订阅源。
func (m *Manager) SubscriptionsListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, map[string]any{"subscriptions": m.loadSubscriptions()})
	}
}

// SubscriptionsAddHandler POST {url, interval_min, target} → 新增订阅源。
func (m *Manager) SubscriptionsAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL         string             `json:"url"`
			IntervalMin int                `json:"interval_min"`
			Target      SubscriptionTarget `json:"target"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		if err := m.AddSubscription(req.URL, req.IntervalMin, req.Target); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// SubscriptionsCountHandler GET ?url= → 返回该订阅使用中实例数（运行中/停止）。
// 用于删除确认弹窗（告知"正在使用的节点有 X 个是否仍删除"）。
func (m *Manager) SubscriptionsCountHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		url := r.URL.Query().Get("url")
		if url == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		group := ""
		if _, meta, err := fetchSubscriptionWithMeta(url); err == nil {
			group = m.groupNameFor(url, meta)
		}
		running, stopped := m.countInstancesForGroup(group)
		writeJSON(w, map[string]any{"group": group, "running": running, "stopped": stopped})
	}
}

// SubscriptionsDeleteHandler POST {url} → 删除订阅源；返回该订阅分组名 + 该分组使用中实例数。
// 删除前先拉取一次该 URL 的元信息以获得分组名（失败则返回空 group，仅删源不释放）。
func (m *Manager) SubscriptionsDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		group := ""
		if _, meta, err := fetchSubscriptionWithMeta(req.URL); err == nil {
			group = m.groupNameFor(req.URL, meta)
		}
		// 统计该分组使用中实例数（订阅缓存节点名 → 实例 Node 匹配）
		running, stopped := m.countInstancesForGroup(group)
		removed, err := m.RemoveSubscription(req.URL)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "removed": removed, "group": group, "running": running, "stopped": stopped})
	}
}

// countInstancesForGroup 统计某订阅分组名下的实例数（按订阅缓存节点名匹配实例 Node）。
// group 为空时返回 0/0（调用方不释放）。
func (m *Manager) countInstancesForGroup(group string) (running, stopped int) {
	if group == "" {
		return 0, 0
	}
	nodeNames := map[string]bool{}
	for _, n := range m.loadSubscriptionCache() {
		if n.Group == group {
			nodeNames[n.Name] = true
		}
	}
	for _, inst := range m.ListInstances() {
		if nodeNames[inst.Node] {
			if inst.Status.State == "Running" {
				running++
			} else {
				stopped++
			}
		}
	}
	return running, stopped
}

// SubscriptionsImportHandler POST {url} → 立即拉取该订阅源（按源目标导入）。
func (m *Manager) SubscriptionsImportHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		n, targetLabel, err := m.ImportSubscriptionNow(req.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "imported": n, "target": targetLabel})
	}
}
