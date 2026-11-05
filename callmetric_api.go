package main

// 调用指标的 HTTP 接口（v2.0.0 增补）。
//
// 四个只读端点，全部 GET + requireAuth：
//   GET /api/admin/call-metrics/summary?days=7
//   GET /api/admin/call-metrics/matrix?days=7
//   GET /api/admin/call-metrics/keys?days=7
//   GET /api/admin/call-metrics/suggestions?days=7
//
// days 支持 1~90，缺省 7。超范围按边界钳制，不报错（前端滑杆随便拖）。

import (
	"net/http"
	"strconv"

	"github.com/6Kmfi6HP/opencode2api/core/manager"
)

// metricDaysParam 解析并钳制 days 参数。
func metricDaysParam(r *http.Request) int {
	const def, max = 7, 90
	s := r.URL.Query().Get("days")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// callMetricSummaryHandler GET /api/admin/call-metrics/summary
func callMetricSummaryHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		days := metricDaysParam(r)
		recs, days := loadMetrics(m, days)
		writeJSONResp(w, buildSummary(recs, days))
	}
}

// callMetricMatrixHandler GET /api/admin/call-metrics/matrix
func callMetricMatrixHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		days := metricDaysParam(r)
		recs, days := loadMetrics(m, days)
		writeJSONResp(w, buildMatrix(recs, days))
	}
}

// callMetricKeysHandler GET /api/admin/call-metrics/keys
func callMetricKeysHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		days := metricDaysParam(r)
		recs, days := loadMetrics(m, days)
		writeJSONResp(w, buildKeyStats(recs, days))
	}
}

// callMetricSuggestionsHandler GET /api/admin/call-metrics/suggestions
func callMetricSuggestionsHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		days := metricDaysParam(r)
		recs, days := loadMetrics(m, days)
		writeJSONResp(w, buildSuggestions(recs, days))
	}
}
