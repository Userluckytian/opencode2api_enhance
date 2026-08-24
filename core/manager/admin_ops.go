// 管理域操作面 HTTP 处理器（/api/admin/*，P4-5 前端走 fetch 的端点集）。
// 只加工 JSON；核心逻辑在各模块。由 main 挂载，沿用既有鉴权中间件。
package manager

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------- 节点

// NodeView 节点（前端契约）。
type NodeView struct {
	Name     string `json:"name"`
	NodeType string `json:"node_type"`
	Server   string `json:"server"`
	Port     uint16 `json:"port"`
	HasCred  bool   `json:"has_cred"`
	Group    string `json:"group"`
}

func (m *Manager) nodeViews() []NodeView {
	sf := m.currentSeams()
	if sf.ListNodes == nil {
		return []NodeView{}
	}
	out := []NodeView{}
	for _, n := range sf.ListNodes() {
		out = append(out, NodeView{
			Name: n.Name, NodeType: n.NodeType, Server: n.Server, Port: n.Port,
			HasCred: n.Password != "" || n.UUID != "", Group: n.Group,
		})
	}
	return out
}

// NodesHandler GET /api/admin/nodes。
func (m *Manager) NodesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.nodeViews())
	}
}

// NodeDeleteHandler POST {name} 删除订阅缓存节点（main 功能 M5；外部 Clash 节点只读跳过）。
func (m *Manager) NodeDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Name == "" {
			writeErr(w, http.StatusBadRequest, "name 必填")
			return
		}
		n, err := m.RemoveSubscriptionNode(req.Name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"removed": n})
	}
}

// NodeDeleteBatchHandler POST {names} 批量删除订阅缓存节点。
func (m *Manager) NodeDeleteBatchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Names []string `json:"names"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Names) == 0 {
			writeErr(w, http.StatusBadRequest, "names 必填")
			return
		}
		n, err := m.RemoveSubscriptionNodes(req.Names)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"removed": n})
	}
}

// ---------------------------------------------------------------- 实例生命周期

// InstancesAddHandler POST {name,port,node,password} → Instance。
// 对齐 Rust add_instance：节点/端口校验、空密码自动生成 sk-、空名称自动命名。
func (m *Manager) InstancesAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Name     string `json:"name"`
			Port     uint16 `json:"port"`
			Node     string `json:"node"`
			Password string `json:"password"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.Node == "" {
			writeErr(w, http.StatusBadRequest, "节点不能为空")
			return
		}
		if req.Port < 1024 {
			writeErr(w, http.StatusBadRequest, "端口需 >= 1024")
			return
		}
		// 空密码自动生成 sk-（Rust add_instance 语义）
		password := req.Password
		if password == "" {
			password = genSkKey()
		}
		// 空名称自动命名（Rust next_auto_name 语义）
		name := req.Name
		if name == "" {
			for i := 1; ; i++ {
				cand := "实例" + itoa16(i)
				if !m.hasInstanceName(cand) {
					name = cand
					break
				}
			}
		}
		inst := Instance{
			Name: name, Port: req.Port, Node: req.Node, Password: password,
			SingboxPort: req.Port + singboxPortOffset, JoinGateway: false,
		}
		if err := m.AddInstance(inst); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		got, _ := m.FindInstance(name)
		writeJSON(w, got)
	}
}

// InstancesRemoveHandler POST {name}。
func (m *Manager) InstancesRemoveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		name, err := decodeName(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := m.RemoveInstanceAlive(m.Run(), name); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 对齐 Rust remove_instance：从池中剔除后同步网关
		if err := m.Gateway().sync(m.Run()); err != nil {
			writeErr(w, http.StatusInternalServerError, "同步网关失败: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// InstancesStartHandler POST {name}。
func (m *Manager) InstancesStartHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		name, err := decodeName(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := m.StartInstance(m.Run(), name); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// InstancesStopHandler POST {name}。
func (m *Manager) InstancesStopHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		name, err := decodeName(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := m.StopInstance(m.Run(), name); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// InstancesRefreshHandler POST {names} → []Instance。
func (m *Manager) InstancesRefreshHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Names []string `json:"names"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad request body")
			return
		}
		writeJSON(w, m.RefreshStates(m.Run(), req.Names))
	}
}

// TestResult 实例测试结果（前端契约）。
type TestResult struct {
	Name       string `json:"name"`
	Port       uint16 `json:"port"`
	OK         bool   `json:"ok"`
	StatusCode *int   `json:"status_code"`
	ModelCount *int   `json:"model_count"`
	Message    string `json:"message"`
	LatencyMS  int64  `json:"latency_ms"`
}

// InstancesTestHandler POST {name} → TestResult（免费模型最小请求实测）。
// 对齐 Rust prepare_test：实例未启动时给出友好提示，而非裸报 TCP 连接错误。
func (m *Manager) InstancesTestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		name, err := decodeName(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		inst, ok := m.FindInstance(name)
		if !ok {
			writeErr(w, http.StatusNotFound, "实例 '"+name+"' 不存在")
			return
		}
		// 前置校验：未运行（含启动中/停止中/错误态）直接返回友好提示（Rust prepare_test 语义）
		if inst.Status.State != "Running" {
			// 对齐 Rust {:?}：Error 态渲染为 Error("msg")
			state := inst.Status.State
			if state == "Error" && len(inst.Status.Error) > 0 {
				state = `Error("` + inst.Status.Error[0] + `")`
			}
			writeJSON(w, TestResult{
				Name:    name,
				Port:    inst.Port,
				OK:      false,
				Message: "实例 '" + name + "' 当前状态为 " + state + "，请先启动后再测试",
			})
			return
		}
		start := time.Now()
		// 对齐 Rust probe_free_completion：超时 10s，成功文案"免费模型最小请求成功"
		status, body, modelCount, freeTested, err := freeCompletion(inst.Port, inst.Password, 10*time.Second)
		res := TestResult{Name: name, Port: inst.Port, LatencyMS: time.Since(start).Milliseconds()}
		sc := status
		res.StatusCode = &sc
		if status >= 200 && status < 300 && err == nil {
			res.OK = true
			if modelCount >= 0 {
				res.ModelCount = &modelCount
			}
			if freeTested {
				res.Message = "免费模型最小请求成功"
			} else {
				res.Message = "models 接口连通（无免费模型可测试）"
			}
		} else if err != nil {
			res.Message = "免费模型请求失败: " + err.Error()
		} else {
			res.Message = "免费模型请求 HTTP " + itoa(uint16(status)) + "：" + truncateBody(body, 240)
		}
		writeJSON(w, res)
	}
}

// truncateBody 截断响应体（Rust truncate 移植：超长加省略号）。
func truncateBody(s []byte, max int) string {
	runes := []rune(string(s))
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return string(runes)
}

// decodeName 读取 {"name":"..."}。
func decodeName(r *http.Request) (string, error) {
	var req struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Name == "" {
		return "", errors.New("请求体需含 name")
	}
	return req.Name, nil
}

// requireMethodOK 校验方法（与 requireMethod 语义一致，返回是否放行）。
func requireMethodOK(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// ---------------------------------------------------------------- 批量

// BatchAddHTTPItem 批量添加入参（前端 BatchAddItem）。
type BatchAddHTTPItem struct {
	Node string  `json:"node"`
	Name *string `json:"name,omitempty"`
	Port *uint16 `json:"port,omitempty"`
}

// BatchAddEntry / BatchAddHTTPResult 批量添加结果（前端契约）。
type BatchAddEntry struct {
	Name string `json:"name"`
	Port uint16 `json:"port"`
	Node string `json:"node"`
}

type BatchAddErr struct {
	Node  string `json:"node"`
	Error string `json:"error"`
}

type BatchAddHTTPResult struct {
	Added      []BatchAddEntry `json:"added"`
	Errors     []BatchAddErr   `json:"errors"`
	AddedCount int             `json:"added_count"`
	ErrorCount int             `json:"error_count"`
}

// BatchAddHandler POST {nodes:[{node,name?,port?}], basePort?, useNodeName?, namePrefix?}。
func (m *Manager) BatchAddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Nodes       []BatchAddHTTPItem `json:"nodes"`
			BasePort    *uint16            `json:"basePort"`
			UseNodeName *bool              `json:"useNodeName"`
			NamePrefix  string             `json:"namePrefix"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		if len(req.Nodes) == 0 {
			writeErr(w, http.StatusBadRequest, "nodes 不能为空")
			return
		}
		basePort := m.instanceBasePort()
		if req.BasePort != nil {
			basePort = *req.BasePort
		}
		useNodeName := false
		if req.UseNodeName != nil {
			useNodeName = *req.UseNodeName
		}
		writeJSON(w, m.httpBatchAdd(req.Nodes, basePort, useNodeName, req.NamePrefix))
	}
}

// BatchOpResult 批量启停结果（前端契约）。
type BatchOpResult struct {
	Success      []string          `json:"success"`
	Errors       map[string]string `json:"errors"`
	SuccessCount int               `json:"success_count"`
	ErrorCount   int               `json:"error_count"`
	// S2: 批量启动跳过已运行实例
	Skipped      []string `json:"skipped,omitempty"`
	SkippedCount int      `json:"skipped_count,omitempty"`
}

func opResult(res map[string]error) BatchOpResult {
	out := BatchOpResult{Success: []string{}, Errors: map[string]string{}}
	for name, err := range res {
		if err == nil {
			out.Success = append(out.Success, name)
			out.SuccessCount++
		} else {
			out.Errors[name] = err.Error()
			out.ErrorCount++
		}
	}
	return out
}

// BatchStartHandler POST {names}。
// S2: 启动仅作用于「非运行中」实例——Running/Starting/Stopping 自动跳过并计入 skipped，
// 不再把这些实例当作启动失败（对齐「一键测试」跳过未启动的语义）。
func (m *Manager) BatchStartHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Names []string `json:"names"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		statusByName := map[string]string{}
		for _, inst := range m.ListInstances() {
			statusByName[inst.Name] = inst.Status.State
		}
		var toStart []string
		var skipped []string
		for _, n := range req.Names {
			switch statusByName[n] {
			case "Running", "Starting", "Stopping":
				skipped = append(skipped, n)
			default:
				toStart = append(toStart, n)
			}
		}
		res := opResult(m.BatchStart(m.Run(), toStart))
		res.Skipped = skipped
		res.SkippedCount = len(skipped)
		writeJSON(w, res)
	}
}

// BatchStopHandler POST {names:[...]}。
func (m *Manager) BatchStopHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Names []string `json:"names"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		writeJSON(w, opm(m.BatchStop(m.Run(), req.Names)))
	}
}

// BatchDeleteHandler POST {names:[...]}。
func (m *Manager) BatchDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Names []string `json:"names"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		writeJSON(w, opm(m.BatchDelete(m.Run(), req.Names)))
	}
}

// ---------------------------------------------------------------- 端口

// PortSuggestHandler GET → 建议端口。
func (m *Manager) PortSuggestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		p, err := m.PortSuggest()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, p)
	}
}

// PortCheckHandler GET ?port=N → PortCheckResult。
func (m *Manager) PortCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		var p uint16
		if _, err := parsePortQuery(r, &p); err != nil {
			writeErr(w, http.StatusBadRequest, "port 必填")
			return
		}
		writeJSON(w, m.PortCheck(p))
	}
}

// ---------------------------------------------------------------- 数据清理 / 自启

// DataCleanHandler POST {level:1|2|3}。
func (m *Manager) DataCleanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Level int `json:"level"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		if err := m.DataClean(m.Run(), m.Gateway(), req.Level); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// AutostartGetHandler / AutostartSetHandler 见 autostart.go（core 承载，走 HTTP）。

// opm 批量结果转换。
var opm = opResult

// httpBatchAdd 批量添加（前端契约形态：按节点去重、自动命名、端口冲突+1）。
func (m *Manager) httpBatchAdd(items []BatchAddHTTPItem, basePort uint16, useNodeName bool, prefix string) BatchAddHTTPResult {
	res := BatchAddHTTPResult{Added: []BatchAddEntry{}, Errors: []BatchAddErr{}}
	haveNode := map[string]bool{}
	for _, e := range m.ListInstances() {
		haveNode[e.Node] = true
	}
	next := 1
	for _, item := range items {
		if item.Node == "" {
			res.Errors = append(res.Errors, BatchAddErr{Node: "", Error: "空节点名"})
			res.ErrorCount++
			continue
		}
		if haveNode[item.Node] {
			res.Errors = append(res.Errors, BatchAddErr{Node: item.Node, Error: "该节点已添加为实例"})
			res.ErrorCount++
			continue
		}
		name := item.Node
		if item.Name != nil && *item.Name != "" {
			name = *item.Name
		} else if !useNodeName || name == "" {
			for {
				name = prefix + "实例" + itoa16(next)
				next++
				if !m.hasInstanceName(name) {
					break
				}
			}
		} else {
			// 节点名作实例名：sanitize（Windows 非法字符 → '-'，Rust 语义一致）
			name = sanitizeInstanceName(item.Node)
		}
		// 名称冲突时自动加后缀（-2、-3…，上限 100；对齐 Rust batch_add）
		finalName := name
		for suffix := uint16(2); m.hasInstanceName(finalName) && suffix <= 100; suffix++ {
			finalName = name + "-" + itoa(suffix)
		}
		name = finalName
		if basePort == 0 {
			basePort = m.instanceBasePort()
		}
		port := basePort
		if item.Port != nil {
			port = *item.Port
		}
		for m.isPortUsedByInstance(port) || m.isPortUsedByInstance(port+singboxPortOffset) || !isPortFree(port) || !isPortFree(port+singboxPortOffset) {
			port++
		}
		inst := Instance{
			Name: name, Port: port, Node: item.Node, Password: genSkKey(),
			SingboxPort: port + singboxPortOffset, JoinGateway: false,
		}
		if err := m.AddInstance(inst); err != nil {
			res.Errors = append(res.Errors, BatchAddErr{Node: item.Node, Error: err.Error()})
			res.ErrorCount++
			continue
		}
		haveNode[item.Node] = true
		res.Added = append(res.Added, BatchAddEntry{Name: name, Port: port, Node: item.Node})
		res.AddedCount++
	}
	return res
}

func (m *Manager) hasInstanceName(name string) bool {
	for _, e := range m.ListInstances() {
		if e.Name == name {
			return true
		}
	}
	return false
}

// itoa16 小工具（与 itoa 一致）。
func itoa16(v int) string {
	return itoa(uint16(v))
}

// sanitizeInstanceName 节点名 → 安全实例名（Rust sanitize_instance_name 移植）：
// Windows 非法字符 / \ : * ? " < > | 与控制字符替换为 '-'，去首尾空格/点，
// 超 40 字符截断，空 → "node"。实例名用于 runtime 目录名，必须文件系统安全。
func sanitizeInstanceName(node string) string {
	var b strings.Builder
	for _, c := range node {
		switch c {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte('-')
		default:
			if c < 0x20 {
				b.WriteByte('-')
			} else {
				b.WriteRune(c)
			}
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, ".")
	if s == "" {
		return "node"
	}
	runes := []rune(s)
	if len(runes) > 40 {
		return string(runes[:40])
	}
	return s
}

// parsePortQuery 从查询参数读取 port。
func parsePortQuery(r *http.Request, out *uint16) (bool, error) {
	s := r.URL.Query().Get("port")
	if s == "" {
		return false, errors.New("missing port")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, errors.New("bad port")
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 || n > 65535 {
		return false, errors.New("bad port")
	}
	*out = uint16(n)
	return true, nil
}

// ---------------------------------------------------------------- 扫描

// ScanStartHandler POST（nodes/apiPort/socksPort/timeout/concurrency）。
func (m *Manager) ScanStartHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Nodes       []string `json:"nodes"`
			APIPort     *uint16  `json:"apiPort"`
			SocksPort   *uint16  `json:"socksPort"`
			Timeout     *int     `json:"timeout"`
			Concurrency *int     `json:"concurrency"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		opts := ScanOptions{Nodes: req.Nodes}
		if req.APIPort != nil {
			opts.APIPort = *req.APIPort
		}
		if req.SocksPort != nil {
			opts.SocksPort = *req.SocksPort
		}
		if req.Timeout != nil {
			opts.TimeoutSec = *req.Timeout
		}
		if req.Concurrency != nil {
			opts.Concurrency = *req.Concurrency
		}
		progress, err := m.Scanner().Start(opts)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, progress)
	}
}

// ScanStatusHandler GET。
func (m *Manager) ScanStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.Scanner().Snapshot())
	}
}

// ScanStopHandler POST。
func (m *Manager) ScanStopHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		writeJSON(w, m.Scanner().RequestStop())
	}
}

// ---------------------------------------------------------------- 网关 / 重启池

// GatewayStatusHandler GET。
func (m *Manager) GatewayStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, m.Gateway().Status(m.Run()))
	}
}

// GatewayModelsRefreshHandler POST 强制同步刷新网关免费模型目录
// （绕过 10s/60s 节流，结果随响应返回；2026-08-24 问题1 待办①）。
func (m *Manager) GatewayModelsRefreshHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		writeJSON(w, m.Gateway().ForceRefreshModels(m.Run()))
	}
}

// GatewayRouteModeHandler POST {mode}。
// 对齐 Rust gateway_set_route_mode：仅接受 smart / failover / round_robin。
func (m *Manager) GatewayRouteModeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Mode == "" {
			writeErr(w, http.StatusBadRequest, "mode 必填")
			return
		}
		if req.Mode != "smart" && req.Mode != "failover" && req.Mode != "round_robin" {
			writeErr(w, http.StatusBadRequest, "路由模式仅支持 smart / failover / round_robin")
			return
		}
		m.Gateway().SetRouteMode(req.Mode)
		// 对齐 Rust gateway_set_route_mode：sync 失败应报错（stop+sync 让配置重写并重启进程）
		if err := m.Gateway().sync(m.Run()); err != nil {
			writeErr(w, http.StatusInternalServerError, "切换路由模式失败: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "mode": req.Mode})
	}
}

// AutoModelConfigHandler GET 返回 / POST 保存 auto 虚拟模型配置。
// 保存即传播到全部子进程配置（运行中子进程 3s 热重载生效），无需重启实例/网关。
func (m *Manager) AutoModelConfigHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, m.AutoModel())
		case http.MethodPost:
			var cfg AutoModelCfg
			if json.NewDecoder(r.Body).Decode(&cfg) != nil {
				writeErr(w, http.StatusBadRequest, "无效的 auto 模型配置")
				return
			}
			cfg.Normalize()
			if err := m.SetAutoModel(cfg); err != nil {
				writeErr(w, http.StatusInternalServerError, "保存 auto 模型配置失败: "+err.Error())
				return
			}
			writeJSON(w, map[string]any{"status": "ok", "config": cfg})
		default:
			writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET / POST")
		}
	}
}

// GatewayStopHandler POST。
func (m *Manager) GatewayStopHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		m.Gateway().stop(m.Run())
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// JoinGatewayHandler POST {name, join}。
func (m *Manager) JoinGatewayHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		var req struct {
			Name string `json:"name"`
			Join bool   `json:"join"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Name == "" {
			writeErr(w, http.StatusBadRequest, "name 必填")
			return
		}
		inst, ok := m.FindInstance(req.Name)
		if !ok {
			writeErr(w, http.StatusNotFound, "实例不存在")
			return
		}
		inst.JoinGateway = req.Join
		if err := m.UpdateInstance(inst); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := m.Gateway().sync(m.Run()); err != nil {
			writeErr(w, http.StatusInternalServerError, "同步网关失败: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	}
}

// RestartPoolHandler POST → RestartPoolResult。
func (m *Manager) RestartPoolHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodOK(w, r, http.MethodPost) {
			return
		}
		writeJSON(w, m.RestartPool(m.Run(), m.Gateway()))
	}
}
