// 订阅源管理（T3：订阅从单条 config 升级为多条列表）。
// 订阅源存储 dataDir/subscriptions.json：每条含 URL / 自动拉取间隔（分钟） / 导入目标 / 分组名。
// 2026-08-16 决策：订阅导入一律只进节点池（拉取+缓存，不创建实例），实例由用户在节点池手动添加；
// target 字段仅保留兼容旧配置，不再影响导入行为。导入时把分组名持久化到源记录，
// 删除订阅/统计据此直接读取（URL 失效也准）。
// 兼容迁移：首次读取时若旧 config.subscribe_url 非空，并入列表为第一条。
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SubscriptionTarget 订阅导入目标（2026-08-16 决策：订阅导入一律只进节点池，不再按目标分流；
// 类型与旧配置保持兼容——旧 solo/pool 值读出后同样只更新节点池缓存，不建实例）。
type SubscriptionTarget string

const (
	// 旧值：建实例-独享（现仅兼容存储，导入行为不变）
	TargetSolo SubscriptionTarget = "solo"
	// 旧值：建实例-进池（现仅兼容存储，导入行为不变）
	TargetPool SubscriptionTarget = "pool"
	// 仅导入节点池（即现唯一行为）
	TargetPoolOnly SubscriptionTarget = "pool-only"
)

// SubscriptionSource 一条订阅源。
type SubscriptionSource struct {
	URL         string             `json:"url"`
	IntervalMin int                `json:"interval_min"` // <=0 = 不自动拉取
	Target      SubscriptionTarget `json:"target"`
	Group       string             `json:"group,omitempty"`       // 订阅分组名（导入时解析写入；删除订阅/统计直接读取）
	NamePinned  bool               `json:"name_pinned,omitempty"` // true=用户手动指定分组名（固定，自动拉取不覆盖）
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
	return writeFileAtomic(m.subscriptionsPath(), data)
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
// names 为可选参数：首个非空值作为用户手动指定的分组名（NamePinned=true 固定）。
func (m *Manager) AddSubscription(url string, intervalMin int, target SubscriptionTarget, names ...string) error {
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
	src := SubscriptionSource{URL: url, IntervalMin: intervalMin, Target: target}
	if len(names) > 0 {
		if name := strings.TrimSpace(names[0]); name != "" {
			src.Group = name
			src.NamePinned = true
		}
	}
	list = append(list, src)
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

// ImportSubscriptionNow 立即执行某条订阅源的导入（2026-08-16 决策：一律只进节点池）。
// 拉取并刷新订阅缓存，不创建任何实例；返回导入节点数与目标标签（恒为「节点池」）。
func (m *Manager) ImportSubscriptionNow(url string) (int, string, error) {
	res, err := m.ImportSubscriptionNowFull(url, false)
	if err != nil {
		return 0, res.Group, err
	}
	return res.Imported, "节点池", nil
}

// ImportSubscriptionNowFull 带 force 与完整摘要（阶段 X T4 契约）：
// shrink 闸拒绝时 err 含 refresh_rejected 哨兵、摘要仍带解析到的数量供 UI 展示「将删除 X%」。
func (m *Manager) ImportSubscriptionNowFull(url string, force bool) (SubscriptionImportResult, error) {
	url = strings.TrimSpace(url)
	res, err := m.importSubscriptionPoolFull(url, force)
	if err != nil {
		return res, err
	}
	m.persistSourceGroup(url, res.Group)
	return res, nil
}

// subscribeFetchConcurrency 订阅拉取并发上限（防多源到点同步尖峰）。
const subscribeFetchConcurrency = 4

// subscribeSupervisorTick 监督循环重读订阅源列表的周期：源增删/改间隔无需重启，
// 最长一个 tick 内生效。
const subscribeSupervisorTick = 30 * time.Second

// subscriptionScheduler 订阅后台调度句柄：唯一启动保护 + 可等待的停止路径。
type subscriptionScheduler struct {
	cancel         context.CancelFunc
	supervisorDone sync.WaitGroup // supervisor 完全退出（关闭全部源循环并等齐）后才返回
	loops          sync.WaitGroup // 各源循环 goroutine
}

// RunAllSubscriptionLoop 启动后台订阅调度：为每个 interval>0 的订阅源启动独立
// goroutine（拉取一次 → 睡该源自己的 IntervalMin），慢源只拖自己。与旧单条入口
// StartSubscribeLoop 经 subscribeLoopOnce 唯一启动保护（二选一，防迁移期双循环
// 并发拉同一 URL）。返回停止函数：取消调度并等待全部源循环退出，返回后无残留
// goroutine（测试/停服干净收尾；生产启动忽略返回值）。
func (m *Manager) RunAllSubscriptionLoop() (stop func()) {
	m.subscribeLoopOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		sched := &subscriptionScheduler{cancel: cancel}
		sched.supervisorDone.Add(1)
		go func() {
			defer sched.supervisorDone.Done()
			m.subscriptionSupervisor(ctx, &sched.loops)
		}()
		m.subscribeSched = sched
	})
	if m.subscribeSched == nil {
		// 被 StartSubscribeLoop 抢先启动（二选一），无独立调度可停。
		return func() {}
	}
	return func() {
		m.subscribeSched.cancel()
		m.subscribeSched.supervisorDone.Wait()
	}
}

// subscriptionSupervisor 订阅源调度监督循环：周期性重读源列表，为每个 interval>0
// 的源维护独立调度 goroutine；已删除/禁用源停掉对应循环，短暂删后重加的源能重新拉起。
// 全部 loops.Add 均在本 goroutine 内同步完成、退出前才 Wait，保证无 Add/Wait 竞争。
func (m *Manager) subscriptionSupervisor(ctx context.Context, loops *sync.WaitGroup) {
	type handle struct {
		stop chan struct{}
		done chan struct{}
	}
	running := map[string]*handle{} // url → 该源循环句柄
	for {
		list := m.loadSubscriptions()
		want := map[string]SubscriptionSource{}
		for _, s := range list {
			if s.IntervalMin > 0 {
				want[s.URL] = s
			}
		}
		// 停掉已删除/禁用的源循环（stop 关闭后循环自退出并通知 done）。
		for url, h := range running {
			if _, ok := want[url]; !ok {
				close(h.stop)
				delete(running, url)
			}
		}
		// 为新增源启动独立循环；已自退出（源曾被删）的旧循环重新拉起。
		for url, s := range want {
			h, ok := running[url]
			if ok {
				select {
				case <-h.done:
					delete(running, url)
				default:
					continue
				}
			}
			h = &handle{stop: make(chan struct{}), done: make(chan struct{})}
			running[url] = h
			loops.Add(1)
			go func(s SubscriptionSource, h *handle) {
				defer loops.Done()
				defer close(h.done)
				m.subscriptionSourceLoop(s, h.stop)
			}(s, h)
		}
		select {
		case <-ctx.Done():
			// 停止全部源循环并等待最后一个在途拉取返回（单次 fetch 超时有界），
			// 确保 stop() 返回后不再有订阅 goroutine 落盘。
			for _, h := range running {
				close(h.stop)
			}
			loops.Wait()
			return
		case <-time.After(subscribeSupervisorTick):
		}
	}
}

// subscriptionSourceLoop 单源调度循环：拉取一次 → 睡该源自己的 IntervalMin。
// 每轮重读源配置（增删/改间隔无需重启）；源被删除或禁用时自退出。
// 并发门控与休眠均 select stop，停止后立即让出、不留尾。
func (m *Manager) subscriptionSourceLoop(s SubscriptionSource, stop <-chan struct{}) {
	for {
		cur, ok := m.subscriptionSourceByURL(s.URL)
		if !ok || cur.IntervalMin <= 0 {
			return
		}
		// 并发拉取门控（≤4），防多源到点同步尖峰。
		select {
		case m.subscribeFetchSem <- struct{}{}:
		case <-stop:
			return
		}
		n, err := m.importSubscriptionForSource(cur)
		<-m.subscribeFetchSem
		if err != nil {
			slog.Warn("订阅自动拉取失败", "url", cur.URL, "error", err)
		} else {
			slog.Info("订阅自动拉取完成", "url", cur.URL, "imported", n)
		}
		select {
		case <-stop:
			return
		case <-time.After(subscriptionWaitOf(cur)):
		}
	}
}

// subscriptionWaitOf 单源调度休眠时长：该源自己的 IntervalMin（下限 30s）。
func subscriptionWaitOf(s SubscriptionSource) time.Duration {
	wait := time.Duration(s.IntervalMin) * time.Minute
	if wait < 30*time.Second {
		wait = 30 * time.Second
	}
	return wait
}

// subscriptionSourceByURL 按 URL 查订阅源（取最新配置）。
func (m *Manager) subscriptionSourceByURL(url string) (SubscriptionSource, bool) {
	for _, s := range m.loadSubscriptions() {
		if s.URL == url {
			return s, true
		}
	}
	return SubscriptionSource{}, false
}

// groupOwner 返回占用分组的其他源描述。exceptPath / exceptURL 用于重载当前源时排除自身。
func (m *Manager) groupOwner(group, exceptPath, exceptURL string) string {
	if group == "" {
		return ""
	}
	for _, source := range m.loadLocalProfiles() {
		if source.Group == group && source.Path != exceptPath {
			return "本地文件源 " + source.Path
		}
	}
	for _, source := range m.loadSubscriptions() {
		if source.Group == group && source.URL != exceptURL {
			return "订阅源 " + source.URL
		}
	}
	return ""
}

// importSubscriptionForSource 自动拉取路径：2026-08-16 决策——一律只进节点池
// （target 值不影响，永不重建实例）；导入后把分组名持久化到源记录。
func (m *Manager) importSubscriptionForSource(s SubscriptionSource) (int, error) {
	n, group, err := m.importSubscriptionPool(s.URL)
	if err != nil {
		return 0, err
	}
	m.persistSourceGroup(s.URL, group)
	return n, nil
}

// persistSourceGroup 把当次解析出的订阅分组名写回源记录（导入成功后调用）。
// 删除订阅/统计直接读持久化 Group，URL 失效后仍能准确对分组。
// 手动固定名（NamePinned）不受自动推导结果覆盖。
func (m *Manager) persistSourceGroup(url, group string) {
	if group == "" {
		return
	}
	list := m.loadSubscriptions()
	changed := false
	for i := range list {
		if list[i].URL == url {
			if list[i].NamePinned {
				return
			}
			if list[i].Group != group {
				list[i].Group = group
				changed = true
			}
		}
	}
	if changed {
		_ = m.saveSubscriptions(list)
	}
}

// pinSourceGroup 手动固定某订阅源的分组名（NamePinned=true，自动拉取不覆盖）。
// 源记录不存在时为空操作（仅保存改名，不创建源）。
func (m *Manager) pinSourceGroup(url, group string) {
	url = strings.TrimSpace(url)
	if url == "" || group == "" {
		return
	}
	list := m.loadSubscriptions()
	changed := false
	for i := range list {
		if list[i].URL == url && (list[i].Group != group || !list[i].NamePinned) {
			list[i].Group = group
			list[i].NamePinned = true
			changed = true
		}
	}
	if changed {
		_ = m.saveSubscriptions(list)
	}
}

// subscriptionGroupFor 解析订阅分组名（删除/统计通用）：
// 优先读源记录已持久化的 Group（导入时写回，URL 失效也准）；
// 未持久化（旧配置/从未导入）时临时拉取元信息解析，失败退回 URL 末段兜底。
func (m *Manager) subscriptionGroupFor(url string) string {
	if s, ok := m.subscriptionSourceByURL(url); ok && s.Group != "" {
		return s.Group
	}
	if _, meta, err := fetchSubscriptionWithMeta(url); err == nil {
		return m.groupNameFor(url, meta)
	}
	return m.groupNameFor(url, SubscriptionMeta{})
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

// SubscriptionsAddHandler POST {url, interval_min, target} → 新增订阅源并立即导入一次。
// P3：保存源成功后同步执行 ImportSubscriptionNow（2026-08-16 决策：一律只进节点池）；
// 导入成功返回 imported/target=节点池；拉取失败（fetch/解析错误）仍返回 HTTP 200 +
// status=ok + imported=0 + error——源已保存，前端据此提示稍后「拉取」重试，不 400 误报保存失败。
func (m *Manager) SubscriptionsAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL         string             `json:"url"`
			IntervalMin int                `json:"interval_min"`
			Target      SubscriptionTarget `json:"target"`
			Name        string             `json:"name"` // 可选：手动指定分组名
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败")
			return
		}
		if err := m.AddSubscription(req.URL, req.IntervalMin, req.Target, req.Name); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 阶段 X T4：首次导入同样返回摘要（usable/unsupported/issues）；旧字段 imported/error 保留。
		res, importErr := m.ImportSubscriptionNowFull(req.URL, false)
		resp := map[string]any{
			"status": "ok", "imported": res.Imported, "target": "节点池",
			"usable": res.Usable, "unsupported": res.Unsupported,
			"issues": res.Issues, "issues_total": res.IssuesTotal,
		}
		if importErr != nil {
			if !strings.Contains(importErr.Error(), "refresh_rejected") {
				res = SubscriptionImportResult{}
			}
			resp["imported"] = res.Imported
			resp["usable"] = res.Usable
			resp["unsupported"] = res.Unsupported
			resp["error"] = importErr.Error()
			if strings.Contains(importErr.Error(), "refresh_rejected") {
				resp["rejected"] = true
			}
		}
		writeJSON(w, resp)
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
		// 分组名优先取源记录持久化 Group（导入时已写入）；未持久化时临时解析
		group := m.subscriptionGroupFor(url)
		running, stopped := m.countInstancesForGroup(group)
		writeJSON(w, map[string]any{"group": group, "running": running, "stopped": stopped})
	}
}

// SubscriptionsDeleteHandler POST {url} → 删除订阅源，并同步清理该订阅占用的全部资源：
// 1) 该分组使用中的实例主动停止；2) 全部停止后从实例池/独享移除释放（含网关同步）；
// 3) 然后删除订阅源；4) 最后清理订阅缓存中该分组节点（节点池页不再残留）。
// 分组名优先取源记录持久化 Group（导入时已写入，URL 失效也准）；未持久化时临时解析。
// 顺序约定：先停实例（使用中的主动停止）→ 全部停止后移除释放 → 删源 → 最后清节点组，
// 节点组是最后一步——任何一步失败都不会把节点组先清掉。
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
		group := m.subscriptionGroupFor(req.URL)
		// 统计该分组使用中实例数（订阅缓存节点名 → 实例 Node 匹配）——须在清理缓存前统计
		running, stopped := m.countInstancesForGroup(group)
		// 受影响实例名快照——清缓存后无法再按缓存节点名反查分组，须在此刻快照
		instances := m.instanceNamesForGroup(group)
		// 1) 主动停止全部相关实例（已停止的幂等跳过）
		// 2) 全部停止后从注册表移除（实例池/独享一并释放）
		released := 0
		if len(instances) > 0 {
			runner := m.Run()
			_ = m.BatchStop(runner, instances)
			for _, err := range m.BatchDelete(runner, instances) {
				if err == nil {
					released++
				}
			}
			// 移除入池（join_gateway）实例后同步统一网关池
			if released > 0 {
				_ = m.Gateway().sync(runner)
			}
		}
		// 3) 删除订阅源
		removed, err := m.RemoveSubscription(req.URL)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 4) 最后清理订阅缓存中该分组节点（节点池页不再残留）
		removedNodes := 0
		if group != "" {
			removedNodes, err = m.RemoveSubscriptionGroupNodes(group)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeJSON(w, map[string]any{"status": "ok", "removed": removed, "group": group, "running": running, "stopped": stopped, "released": released, "removed_nodes": removedNodes, "instances": instances})
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

// instanceNamesForGroup 返回某订阅分组名下已创建的实例名（删除订阅时前端按此释放）。
// 须在清理缓存前调用（依赖订阅缓存节点名 → 实例 Node 匹配）。
func (m *Manager) instanceNamesForGroup(group string) []string {
	if group == "" {
		return nil
	}
	nodeNames := map[string]bool{}
	for _, n := range m.loadSubscriptionCache() {
		if n.Group == group {
			nodeNames[n.Name] = true
		}
	}
	var names []string
	for _, inst := range m.ListInstances() {
		if nodeNames[inst.Node] {
			names = append(names, inst.Name)
		}
	}
	return names
}

// SubscriptionsImportHandler POST {url, name?} → 立即拉取该订阅源（一律只进节点池）。
// name 可选：非空时先固定该源的分组名（NamePinned=true），导入节点即归入此组。
func (m *Manager) SubscriptionsImportHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			URL   string `json:"url"`
			Name  string `json:"name"`
			Force bool   `json:"force"` // 阶段 X B3：越过删除保护闸强制刷新（用户在确认弹窗点「仍要刷新」）
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" {
			writeErr(w, http.StatusBadRequest, "url 必填")
			return
		}
		if name := strings.TrimSpace(req.Name); name != "" {
			m.pinSourceGroup(req.URL, name)
		}
		// 阶段 X T4：一次拉取返回完整摘要（usable/unsupported/issues），两种入口共用响应形态。
		res, importErr := m.ImportSubscriptionNowFull(req.URL, req.Force)
		resp := map[string]any{
			"status": "ok", "imported": res.Imported, "target": "节点池",
			"usable": res.Usable, "unsupported": res.Unsupported,
			"issues": res.Issues, "issues_total": res.IssuesTotal,
		}
		if res.Truncated {
			resp["issues_truncated"] = true
		}
		if importErr != nil {
			if !strings.Contains(importErr.Error(), "refresh_rejected") {
				// 拉取/解析真失败：保持旧契约（源已保存、imported=0、error 提示重试）。
				res = SubscriptionImportResult{}
			}
			resp["imported"] = res.Imported
			resp["usable"] = res.Usable
			resp["unsupported"] = res.Unsupported
			resp["error"] = importErr.Error()
			// 删除保护闸拒绝：语义是「保护成功」而非拉取失败，前端据此提供「强制刷新」。
			if strings.Contains(importErr.Error(), "refresh_rejected") {
				resp["rejected"] = true
			}
		}
		writeJSON(w, resp)
	}
}
