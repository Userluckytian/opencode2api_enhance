package main

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// dailyResetHour 每日额度/窗口的统一重置时刻（本地时区 8:00）。
// 选在早上而非 0 点，是为了让「昨天领的 Key」在上班时间点失效后，
// 使用者当天早上就能重新领取，而不是半夜悄悄重置。
const dailyResetHour = 8

// nextDailyReset 返回 now 之后的下一次重置时刻（本地时区当天 8:00 或次日 8:00）。
func nextDailyReset(now time.Time) time.Time {
	loc := now.Location()
	today8 := time.Date(now.Year(), now.Month(), now.Day(), dailyResetHour, 0, 0, 0, loc)
	if now.Before(today8) {
		return today8
	}
	return today8.AddDate(0, 0, 1)
}

// dailyResetDate 返回 now 所属的「额度日」的日期串。
// 8:00 之前算前一天（8:00~次日 8:00 为同一个额度日）。
func dailyResetDate(now time.Time) string {
	return nextDailyReset(now).AddDate(0, 0, -1).Format("2006-01-02")
}

// isSameDailyPeriod 判断两个时刻是否落在同一个额度日（以 8:00 为界）。
func isSameDailyPeriod(a, b time.Time) bool {
	return dailyResetDate(a) == dailyResetDate(b)
}

// alignToGrid 把时刻对齐到以 8:00 为起点的固定时段网格，返回 [窗口开始, 窗口结束]。
//
// 例如窗口长度 1440 分钟（一天）：8:00 → 次日 8:00；
// 长度 120 分钟（两小时）：8:00→10:00、10:00→12:00 ……
// 这样「重置时间」对使用者是可预期的，而不是从首次使用起算的滚动窗口。
func alignToGrid(now time.Time, windowMinutes int) (start, end time.Time) {
	if windowMinutes <= 0 {
		return now, time.Time{}
	}
	// 防御性上界（finding 565）：windowMinutes 来自 IP 策略配置，历史上没有上界，
	// 一旦写入 9007199254740992 这种值，下面 `time.Duration(windowMinutes) * time.Minute`
	// 会在 int64 纳秒上溢出成 0 或负数，/ 运算直接 panic（divide by zero），
	// 之后每个 /v1 请求都 502。这里做最后一道兜底，即使配置层被绕过也不会打挂服务。
	if windowMinutes > MaxWindowMinutes {
		windowMinutes = MaxWindowMinutes
	}
	loc := now.Location()
	// 以当天的 8:00 为网格原点，向前推算所在时段
	origin := time.Date(now.Year(), now.Month(), now.Day(), dailyResetHour, 0, 0, 0, loc)
	if now.Before(origin) {
		origin = origin.AddDate(0, 0, -1)
	}
	step := time.Duration(windowMinutes) * time.Minute
	if step <= 0 {
		// 理论上不可达（上面已夹到 1 分钟~一周），但绝不允许把 0 当除数。
		step = time.Duration(MaxWindowMinutes) * time.Minute
	}
	elapsed := now.Sub(origin)
	idx := int64(elapsed / step)
	start = origin.Add(time.Duration(idx) * step)
	end = start.Add(step)
	return start, end
}

// generateSecret 生成一个随机的反代共享密钥。
func generateSecret() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}
