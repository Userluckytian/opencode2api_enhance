package contract

// PoolSnapshot 是请求失败时的池状态快照（供日志挖掘/分析视图使用）。
type PoolSnapshot struct {
	Total        int    `json:"total"`
	Available    int    `json:"available"`
	Cooling      int    `json:"cooling"`
	BadQuota     int    `json:"bad_quota"`
	BadAuth      int    `json:"bad_auth"`
	BadLink      int    `json:"bad_link"`
	BreakerOpen  int    `json:"breaker_open"`
	Selected     string `json:"selected,omitempty"`
	SelectedState string `json:"selected_state,omitempty"`
	SelectedUntil string `json:"selected_until,omitempty"`
}

// NodeState 是本次失败所涉及出口节点的健康状态快照。
type NodeState struct {
	Addr          string `json:"addr"`
	Class         string `json:"class,omitempty"`
	Reason        string `json:"reason,omitempty"`
	BadCount      int    `json:"bad_count,omitempty"`
	CooldownUntil string `json:"cooldown_until,omitempty"`
}
