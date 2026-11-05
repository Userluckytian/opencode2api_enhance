package manager

// 引擎 stdout → 调用指标的增量消费（v2.0.0 增补）。
//
// 为什么要增量而不是每次全量重解析：stdout.log 是引擎**追加写**的文件，
// 长跑几天后可以到几十 MB。全量重扫既慢又会产生重复指标行（同一条
// request_routed 被写入指标文件多次），所以记一个字节偏移，只消费新增部分。
//
// 偏移文件：<runtime>/call-metrics/.offset.json，内容 {"offset": N, "size": M}。
// 判定文件被轮转/截断：当前 size < 记录的 size 时视为重置，偏移归零重扫。
//
// 重复兜底：即使偏移失效导致重扫，聚合侧也可按 req_id 去重（见 ReadCallMetrics
// 的调用方），但这里优先保证「正常路径不重复」。

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// engineMetricOffset 偏移文件结构。
type engineMetricOffset struct {
	// Offset 已消费到的字节位置。
	Offset int64 `json:"offset"`
	// Size 记录时的文件大小。
	Size int64 `json:"size"`
	// Head 记录时文件前 64 字节的指纹（十六进制）。
	//
	// 为什么需要它：只比 Size 无法识别「截断后又写回同样长度」的场景
	//（轮转 + 重启恰好长度接近时），会导致偏移停在错误位置、新记录被跳过。
	// 头部指纹变了就说明不是同一个文件内容，必须重扫。
	Head string `json:"head,omitempty"`
	// UpdatedAt 最后一次消费时间。
	UpdatedAt string `json:"updated_at,omitempty"`
}

// engineMetricOffsetName 偏移文件名（放在指标目录内，随指标一起清）。
const engineMetricOffsetName = ".offset.json"

// engineMetricConsumeMu 串行化消费：两个 ticker 同时跑会导致偏移竞争、
// 甚至同一批行被写两次。
var engineMetricConsumeMu sync.Mutex

// callMetricOffsetPath 返回偏移文件路径。
func (m *Manager) callMetricOffsetPath() string {
	return filepath.Join(m.CallMetricsDir(), engineMetricOffsetName)
}

// loadEngineMetricOffset 读偏移；文件缺失或损坏按 0 处理（下次全量重扫）。
func (m *Manager) loadEngineMetricOffset() engineMetricOffset {
	data, err := os.ReadFile(m.callMetricOffsetPath())
	if err != nil {
		return engineMetricOffset{}
	}
	var off engineMetricOffset
	if json.Unmarshal(data, &off) != nil {
		return engineMetricOffset{}
	}
	if off.Offset < 0 {
		off.Offset = 0
	}
	return off
}

// saveEngineMetricOffset 落盘偏移（失败只返回 error，不致命）。
func (m *Manager) saveEngineMetricOffset(off engineMetricOffset) error {
	if err := os.MkdirAll(m.CallMetricsDir(), 0o755); err != nil {
		return err
	}
	off.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	data, err := json.Marshal(off)
	if err != nil {
		return err
	}
	return os.WriteFile(m.callMetricOffsetPath(), data, 0o644)
}

// ConsumeEngineMetrics 增量消费引擎 stdout，把新增的 request_routed 事件
// 转成指标行并落盘。返回本次写入的条数。
//
// 同时 join 同批次里的 session_identity_rotated（按 request_id），
// 给对应指标补 Rotated / ProxyClass。
func (m *Manager) ConsumeEngineMetrics() (int, error) {
	if m == nil {
		return 0, nil
	}
	path := m.EngineStdoutLogPath()

	// 加锁放在 stat 之前：避免两个消费者同时算出相同偏移再各写一遍。
	engineMetricConsumeMu.Lock()
	defer engineMetricConsumeMu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		// 引擎未启动 / 没有 stdout.log：不算错误。
		return 0, nil
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()

	off := m.loadEngineMetricOffset()
	start := off.Offset
	headNow := fileHeadHash(path, 64)
	// 判定是否需要重扫，任一成立即可：
	//   1) 文件变小（明显截断）；
	//   2) 头部指纹与上次记录不同（轮转后重写，即使长度相近）；
	//   3) 记录的偏移已越过文件尾（异常）。
	if size < off.Size || start > size || (off.Head != "" && off.Head != headNow) {
		start = 0
	}
	if start == size {
		// 没有新内容
		return 0, nil
	}

	if _, err := f.Seek(start, 0); err != nil {
		return 0, err
	}

	// 第一遍：收集本批次的 request_routed 与轮换事件，按 req_id 关联。
	type rotationInfo struct {
		Rotated    bool
		ProxyClass string
	}
	rotations := map[string]rotationInfo{}
	writes := make([]CallMetric, 0, 64)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var consumed int64
	for sc.Scan() {
		raw := sc.Bytes()
		consumed += int64(len(raw)) + 1 // +1 换行符
		line := strings.TrimSpace(string(raw))
		if line == "" || line[0] != '{' {
			continue
		}
		var rawLine engineLogLine
		if json.Unmarshal([]byte(line), &rawLine) != nil {
			continue
		}
		switch rawLine.Event {
		case "request_routed":
			writes = append(writes, engineLogLineToMetric(rawLine))
		case "session_identity_rotated":
			if rawLine.RequestID == "" {
				continue
			}
			info := rotations[rawLine.RequestID]
			info.Rotated = true
			if info.ProxyClass == "" {
				info.ProxyClass = proxyClassOf(rawLine.Proxy)
			}
			rotations[rawLine.RequestID] = info
		}
	}
	scanErr := sc.Err()

	// 回填轮换信息
	for i := range writes {
		if info, ok := rotations[writes[i].ReqID]; ok {
			writes[i].Rotated = true
			if writes[i].ProxyClass == "" {
				writes[i].ProxyClass = info.ProxyClass
			}
		}
	}

	// 落盘指标
	for _, rec := range writes {
		if err := m.AppendCallMetric(rec); err != nil {
			// 指标是旁路：写失败就停，但要先把偏移推进到已消费位置，
			// 避免同一批反复重试造成死循环。
			_ = m.saveEngineMetricOffset(engineMetricOffset{Offset: start + consumed, Size: size, Head: headNow})
			return 0, err
		}
	}

	// 只有整批都写成功才推进偏移
	newOff := start + consumed
	if scanErr != nil && newOff > size {
		newOff = size
	}
	_ = m.saveEngineMetricOffset(engineMetricOffset{Offset: newOff, Size: size, Head: headNow})

	return len(writes), nil
}

// fileHeadHash 返回文件前 n 字节的十六进制指纹（用于识别文件是否被替换）。
// 读失败返回空串（调用方据此跳过指纹判定，退化为只比 size）。
func fileHeadHash(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	read, _ := f.Read(buf)
	if read <= 0 {
		return ""
	}
	return hex.EncodeToString(buf[:read])
}

// proxyClassOf 从轮换事件的 proxy 字段提取「类别 + IP 段」。
//
// 引擎给的是形如 `warp:2a09:bac5:1ef3:3046::/64` 或普通 host:port。
// 我们只保留可聚合的部分：warp 出口保留到 /64 前缀（去掉具体后缀，
// 因为同一 /64 段对上游是同一个出口画像），其它按 host 归类。
func proxyClassOf(proxy string) string {
	p := strings.TrimSpace(proxy)
	if p == "" {
		return ""
	}
	// warp:xxxx::/64 —— 保留 warp 前缀 + 前 4 段。
	// IPv6 每段 16 bit，/64 恰好是前 4 段；少一段会把不同 /64 的出口合成一类，
	// 多一段则同一 /64 被拆成多个，两者都会算错「出口段」维度。
	if strings.HasPrefix(p, "warp:") {
		addr := strings.TrimPrefix(p, "warp:")
		parts := strings.Split(addr, ":")
		if len(parts) >= 4 {
			return "warp:" + strings.Join(parts[:4], ":") + "::/64"
		}
		return "warp"
	}
	// 其它代理：取 host（去端口）
	if h, _, ok := strings.Cut(p, ":"); ok {
		return h
	}
	return p
}

// engineLogLineToMetric 把一行 request_routed 转成指标。
//
// 与 engineRecordFromLine 的区别：那个是给「日志页」用的展示记录（含 events 时间线、
// 合成错误文案）；这里是给聚合用的瘦行，只保留可 group by 的维度。
func engineLogLineToMetric(raw engineLogLine) CallMetric {
	status := "fail"
	switch raw.Outcome {
	case "success":
		status = "ok"
	case "client_canceled":
		status = "canceled"
	}
	if status != "ok" && raw.Status >= 200 && raw.Status < 300 {
		status = "ok"
	}

	legs := parseEngineLegs(raw.Legs)
	// 上游真实状态取最后一跳（顶层 status 在 client_canceled 时恒为 499）
	upstream := raw.Status
	for i := len(legs) - 1; i >= 0; i-- {
		if legs[i].Status > 0 {
			upstream = legs[i].Status
			break
		}
	}

	metricLegs := make([]CallMetricLeg, 0, len(legs))
	for _, l := range legs {
		metricLegs = append(metricLegs, CallMetricLeg{Key: l.Key, Status: l.Status, DurMS: l.DurMS})
	}

	rec := CallMetric{
		TS:               raw.Time,
		ReqID:            raw.RequestID,
		Source:           "engine",
		Model:            raw.Model,
		Channel:          raw.Channel,
		Tier:             raw.Tier,
		KeyID:            raw.KeyID,
		Status:           status,
		UpstreamStatus:   upstream,
		Outcome:          raw.Outcome,
		DurationMS:       raw.DurationMS,
		PromptTokens:     raw.In,
		CompletionTokens: raw.Out,
		Attempts:         raw.Attempts,
		Legs:             metricLegs,
		Stream:           raw.Stream,
		Path:             raw.Path,
	}
	if status != "ok" {
		rec.ErrorClass = engineClassOf(upstream)
		if rec.ErrorClass == "" {
			if strings.TrimSpace(raw.RouteError) != "" {
				rec.ErrorClass = "other"
			} else if raw.Outcome == "client_canceled" {
				rec.ErrorClass = "timeout"
			}
		}
	}
	return rec
}
