import { Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import clsx from 'clsx'
import { BarChart3, ChevronDown, ChevronRight, RefreshCw, RotateCcw, Inbox, CalendarDays, Loader2 } from 'lucide-react'
import { api, type StatsSummary, type DayStats } from '../lib/api'

/** 千分位格式化 */
const fmt = (n: number) => n.toLocaleString('en-US')

/** 统一网关在统计表中的展示名（与后端 unifiedGatewayName 一致） */
const GATEWAY_DISPLAY = '统一网关'
/** 按天趋势条最多展示的天数 */
const TREND_DAYS = 14

/** 调用日志按天聚合的单个数据点 */
type DayTrendPoint = {
  date: string
  total: number
  ok: number
  fail: number
}

/** 成功/失败迷你条形图：绿=成功、红=失败，按占比宽度；无数据时灰色空态 */
function MiniBar({ ok, fail }: { ok: number; fail: number }) {
  const total = ok + fail
  if (total === 0) {
    return <div className="h-2 w-32 rounded-full bg-zinc-100" title="暂无调用日志" />
  }
  return (
    <div className="w-32">
      <div className="flex h-2 w-full overflow-hidden rounded-full bg-zinc-100">
        <div className="h-full bg-green-500" style={{ width: `${(ok / total) * 100}%` }} title={`成功 ${ok}`} />
        <div className="h-full bg-red-500" style={{ width: `${(fail / total) * 100}%` }} title={`失败 ${fail}`} />
      </div>
      <div className="mt-0.5 flex justify-between text-[10px] tabular-nums">
        <span className="text-green-600">{ok}</span>
        <span className="text-red-500">{fail}</span>
      </div>
    </div>
  )
}

function Card({
  label,
  value,
  accent,
  children,
}: {
  label: string
  value?: string
  accent?: boolean
  children?: ReactNode
}) {
  return (
    <div className="flex-1 min-w-[150px] bg-white rounded-[16px] border border-zinc-200 shadow-sm p-4">
      <div className="text-[12px] text-zinc-500 mb-1">{label}</div>
      {children ?? (
        <div className={clsx('text-[22px] font-semibold tabular-nums', accent ? 'text-teal-700' : 'text-zinc-900')}>
          {value}
        </div>
      )}
    </div>
  )
}

export default function StatsPage({
  toast,
}: {
  toast: (msg: string, ok?: boolean) => void
}) {
  const [stats, setStats] = useState<StatsSummary | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  // 重置二次确认弹窗
  const [showResetConfirm, setShowResetConfirm] = useState(false)
  // 「清除已删除节点」默认勾选
  const [clearDeleted, setClearDeleted] = useState(true)
  // 按天查看：日期列表（来自调用日志）+ 选中日 + 当日聚合
  const [dates, setDates] = useState<string[]>([])
  const [day, setDay] = useState('')
  const [dayStats, setDayStats] = useState<DayStats | null>(null)
  const [dayBusy, setDayBusy] = useState(false)
  // 迷你条图数据（来自调用日志聚合）：每来源成功/失败、按天请求量/成功率
  const [insOkFail, setInsOkFail] = useState<Record<string, { ok: number; fail: number }>>({})
  const [dayTrend, setDayTrend] = useState<DayTrendPoint[]>([])
  // G29: 按天请求序号——快速切换日期时过期响应丢弃，防止数据串日期
  const dayReqSeq = useRef(0)
  // M9: 轮询代次守卫——load/loadDates 各记一代，响应后比对，过期响应丢弃（不叠加、旧快照不覆盖新状态）
  const loadGen = useRef(0)
  const datesGen = useRef(0)

  // 提取调用日志中出现过的日期（新→旧）供按天筛选，同时聚合迷你条图数据
  const loadDates = useCallback(async () => {
    const gen = ++datesGen.current
    try {
      const recs = await api.getCallLog(5000)
      if (gen !== datesGen.current) return
      const s = new Set<string>()
      const bySrc = new Map<string, { ok: number; fail: number }>()
      const byDay = new Map<string, { total: number; ok: number }>()
      for (const l of recs) {
        const d = (l.ts || '').slice(0, 10)
        if (d) s.add(d)
        const ok = l.status === 'ok'
        // 来源标注：空 = 统一网关；否则为独享实例名
        const src = (l.source || '').trim() || GATEWAY_DISPLAY
        const a = bySrc.get(src)
        if (a) {
          if (ok) a.ok++
          else a.fail++
        } else {
          bySrc.set(src, ok ? { ok: 1, fail: 0 } : { ok: 0, fail: 1 })
        }
        if (d) {
          const b = byDay.get(d)
          if (b) {
            b.total++
            if (ok) b.ok++
          } else {
            byDay.set(d, ok ? { total: 1, ok: 1 } : { total: 1, ok: 0 })
          }
        }
      }
      setDates([...s].sort().reverse())
      setInsOkFail(Object.fromEntries(bySrc))
      setDayTrend(
        [...byDay]
          .map(([date, v]) => ({ date, total: v.total, ok: v.ok, fail: v.total - v.ok }))
          .sort((x, y) => (x.date < y.date ? 1 : -1))
          .slice(0, TREND_DAYS),
      )
    } catch {
      if (gen !== datesGen.current) return
      /* 忽略：无日志时日期为空，迷你条图置空 */
      // G28: 一并清空日期——拉取失败时不残留陈旧日期/按天选择
      setDates([])
      setInsOkFail({})
      setDayTrend([])
    }
  }, [])

  // 选择日期 → 拉取当日聚合；切回全部 → 恢复累计视图
  const doSelectDay = async (d: string) => {
    setDay(d)
    // G29: 每个选择（含切回全部）都推进序号——在途旧响应落地时比对序号，过期丢弃
    const seq = ++dayReqSeq.current
    if (!d) {
      setDayStats(null)
      return
    }
    setDayBusy(true)
    try {
      const s = await api.statsByDay(d)
      if (seq !== dayReqSeq.current) return
      setDayStats(s)
    } catch (e) {
      if (seq !== dayReqSeq.current) return
      toast(String(e), false)
    } finally {
      if (seq === dayReqSeq.current) setDayBusy(false)
    }
  }

  // G27: dates 收窄后过期的按天选择自动复位（同时作废在途按天请求，防过期响应落库）
  useEffect(() => {
    if (day && !dates.includes(day)) {
      dayReqSeq.current++
      setDay('')
      setDayStats(null)
    }
  }, [dates, day])

  // G31: toast 用 ref 封装（App 的 showToast 每次渲染重建）——load 依赖不含 toast，轮询定时器不因 toast 重启
  const toastRef = useRef(toast)
  toastRef.current = toast

  const load = useCallback(
    async (silent = true) => {
      const gen = ++loadGen.current
      try {
        const s = await api.getStats()
        if (gen !== loadGen.current) return
        setStats(s)
        setError(null)
      } catch (e) {
        if (gen !== loadGen.current) return
        if (!silent) toastRef.current(String(e), false)
        else setError(String(e))
      }
    },
    [],
  )

  // 自动轮询（静默，5s）：token 卡与迷你条/趋势/日期一并刷新（G28）
  useEffect(() => {
    void load()
    void loadDates()
    const t = setInterval(() => {
      void load(true)
      void loadDates()
    }, 5000)
    return () => clearInterval(t)
  }, [load, loadDates])

  // 手动刷新（带 loading）
  const doRefresh = async () => {
    setRefreshing(true)
    await load(false)
    await loadDates()
    setRefreshing(false)
  }

  // 重置全部统计：运行中的实例/网关走 HTTP 复位，未运行的覆写磁盘文件
  const doReset = async (clearDeleted: boolean) => {
    setResetting(true)
    setShowResetConfirm(false)
    try {
      const r = await api.resetStats(clearDeleted)
      const fail = r.failed.length > 0 ? `，失败 ${r.failed.length}：${r.failed.join('；')}` : ''
      const del = r.deleted_count > 0 ? `，清除历史统计 ${r.deleted_count} 项` : ''
      toast(`已重置 ${r.reset_count} 项统计${del}${fail}`, r.failed.length === 0)
      await load(false)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setResetting(false)
    }
  }

  const toggleExpand = (name: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  // 报表导出（已移除：无使用场景）
  const instances = stats?.instances ?? []
  const isEmpty = !stats || instances.length === 0

  return (
    <div className="p-6 flex flex-col gap-5">
      {/* 顶部工具条 */}
      <div className="flex items-center justify-between">
        <h1 className="text-[16px] font-semibold text-zinc-900 flex items-center gap-2">
          <BarChart3 size={18} className="text-teal-700" />
          Token 统计
        </h1>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => setShowResetConfirm(true)}
            disabled={resetting || !stats}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-white border border-zinc-200 text-zinc-600 text-[13px] font-medium hover:bg-zinc-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <RotateCcw size={14} className={resetting ? 'animate-spin' : ''} />
            {resetting ? '重置中…' : '重置统计'}
          </button>
          <button
            type="button"
            onClick={doRefresh}
            disabled={refreshing}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-zinc-900 text-white text-[13px] font-medium hover:bg-zinc-700 transition-colors disabled:cursor-not-allowed disabled:opacity-50"
          >
            <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
            {refreshing ? '刷新中…' : '刷新'}
          </button>
        </div>
      </div>

      {/* 按天查看 */}
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-1.5 rounded-lg border border-zinc-200 bg-white px-2.5 py-1.5">
          <CalendarDays size={14} className="text-zinc-400" />
          <select
            value={day}
            onChange={(e) => void doSelectDay(e.target.value)}
            className="text-[13px] text-zinc-600 bg-transparent outline-none"
            title="按日期查看统计（数据来自统一网关调用日志）"
          >
            <option value="">全部日期（累计）</option>
            {dates.map((d) => (
              <option key={d} value={d}>
                {d}
              </option>
            ))}
          </select>
        </div>
        {dayBusy ? (
          <Loader2 size={14} className="animate-spin text-zinc-400" />
        ) : (
          day && (
            <span className="text-[12px] text-zinc-400">
              {day} · 按统一网关调用日志统计
            </span>
          )
        )}
      </div>

      {/* 总览卡片：按天 6 卡 / 累计 4 卡 */}
      <div className="flex flex-wrap gap-4">
        {day && dayStats ? (
          <>
            <Card label="当日请求数" value={fmt(dayStats.total_requests)} />
            <Card label="成功" value={fmt(dayStats.ok_requests)} />
            <Card label="失败" value={fmt(dayStats.fail_requests)} />
            <Card label="输入 Token" value={fmt(dayStats.total_prompt_tokens)} />
            <Card label="输出 Token" value={fmt(dayStats.total_completion_tokens)} />
            <Card label="总 Token" value={fmt(dayStats.total_tokens)} accent />
          </>
        ) : (
          <>
            <Card label="总请求数" value={fmt(stats?.total_requests ?? 0)} />
            <Card label="总输入 Token" value={fmt(stats?.total_prompt_tokens ?? 0)} />
            <Card label="总输出 Token" value={fmt(stats?.total_completion_tokens ?? 0)} />
            <Card label="总 Token" value={fmt(stats?.total_tokens ?? 0)} accent />
          </>
        )}
      </div>

      {/* 按天视图：请求量 / 成功率趋势条（纯 CSS 柱状，复用 Card 组件） */}
      {day && dayStats && (
        <Card label="请求量 / 成功率趋势（近 14 天）">
          {dayTrend.length === 0 ? (
            <p className="text-[12px] text-zinc-400">暂无调用日志</p>
          ) : (
            <>
              {(() => {
                const maxTotal = dayTrend.reduce((m, p) => Math.max(m, p.total), 0)
                return (
                  <div className="flex h-24 items-end gap-1">
                    {dayTrend.map((p) => {
                      const okPct = p.total > 0 ? (p.ok / p.total) * 100 : 0
                      const h = maxTotal > 0 ? (p.total / maxTotal) * 100 : 0
                      return (
                        <div
                          key={p.date}
                          className="flex h-full min-w-0 flex-1 flex-col justify-end"
                          title={`${p.date} · ${fmt(p.total)} 请求 · 成功率 ${Math.round(okPct)}%`}
                        >
                          {p.total === 0 ? (
                            <div className="h-[2px] rounded bg-zinc-200" />
                          ) : (
                            <div className="w-full overflow-hidden rounded-t-sm" style={{ height: `${h}%` }}>
                              <div className="flex h-full w-full flex-col-reverse">
                                <div className="w-full bg-green-500" style={{ height: `${okPct}%` }} />
                                <div className="w-full bg-red-500" style={{ height: `${100 - okPct}%` }} />
                              </div>
                            </div>
                          )}
                        </div>
                      )
                    })}
                  </div>
                )
              })()}
              <div className="mt-2 flex items-center gap-3 text-[11px] text-zinc-500">
                <span className="flex items-center gap-1">
                  <span className="inline-block h-2 w-2 rounded-sm bg-green-500" />
                  成功
                </span>
                <span className="flex items-center gap-1">
                  <span className="inline-block h-2 w-2 rounded-sm bg-red-500" />
                  失败
                </span>
                <span className="text-zinc-400">柱高 = 当日请求量 · 绿/红占比 = 成功率</span>
              </div>
            </>
          )}
        </Card>
      )}

      {error && !stats && (
        <div className="text-[13px] text-red-600 bg-red-50 border border-red-100 rounded-xl px-4 py-3">
          加载失败：{error}
        </div>
      )}

      {/* 实例表格：按天明细 or 累计实例表 */}
      <div className="bg-white rounded-[16px] border border-zinc-200 shadow-sm p-5">
        {day && dayStats ? (
          <div className="space-y-5">
            {/* 当日模型用量 */}
            <div>
              <div className="text-[13px] font-semibold text-zinc-800 mb-2">当日模型用量</div>
              {dayStats.by_model.length === 0 ? (
                <p className="text-[12px] text-zinc-400">当日无调用记录</p>
              ) : (
                <table className="w-full text-[13px]">
                  <thead>
                    <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
                      <th className="py-2 pr-3 font-medium">模型</th>
                      <th className="py-2 pr-3 font-medium text-right">请求数</th>
                      <th className="py-2 pr-3 font-medium text-right">输入 Token</th>
                      <th className="py-2 pr-3 font-medium text-right">输出 Token</th>
                      <th className="py-2 font-medium text-right">总计</th>
                    </tr>
                  </thead>
                  <tbody>
                    {dayStats.by_model.map((m) => (
                      <tr key={m.model} className="border-b border-zinc-50">
                        <td className="py-2 pr-3 text-zinc-800">{m.model}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmt(m.requests)}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmt(m.prompt_tokens)}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmt(m.completion_tokens)}</td>
                        <td className="py-2 text-right tabular-nums font-medium text-zinc-900">{fmt(m.total_tokens)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>

            {/* 当日节点用量 */}
            {dayStats.by_node.length > 0 && (
              <div>
                <div className="text-[13px] font-semibold text-zinc-800 mb-2">当日节点用量（经统一网关路由）</div>
                <table className="w-full text-[13px]">
                  <thead>
                    <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
                      <th className="py-2 pr-3 font-medium">节点</th>
                      <th className="py-2 pr-3 font-medium text-right">请求数</th>
                      <th className="py-2 pr-3 font-medium text-right">输入 Token</th>
                      <th className="py-2 pr-3 font-medium text-right">输出 Token</th>
                      <th className="py-2 font-medium text-right">总计</th>
                    </tr>
                  </thead>
                  <tbody>
                    {dayStats.by_node.map((n) => (
                      <tr key={n.addr} className="border-b border-zinc-50">
                        <td className="py-2 pr-3 text-zinc-800">{n.name}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmt(n.requests)}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmt(n.prompt_tokens)}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmt(n.completion_tokens)}</td>
                        <td className="py-2 text-right tabular-nums font-medium text-zinc-900">{fmt(n.total_tokens)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ) : (
          <>
        {isEmpty && !error ? (
          <div className="py-12 flex flex-col items-center gap-2 text-zinc-400">
            <Inbox size={28} strokeWidth={1.5} />
            <span className="text-[13px]">暂无统计数据，启动实例并产生对话后会自动记录</span>
          </div>
        ) : (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
                <th className="py-2 pr-3 font-medium">实例</th>
                <th className="py-2 pr-3 font-medium text-right">请求数</th>
                <th className="py-2 pr-3 font-medium">成功/失败</th>
                <th className="py-2 pr-3 font-medium text-right">输入 Token</th>
                <th className="py-2 pr-3 font-medium text-right">输出 Token</th>
                <th className="py-2 pr-3 font-medium text-right">总计</th>
                <th className="py-2 font-medium">状态</th>
              </tr>
            </thead>
            <tbody>
              {instances.map((ins) => {
                const open = expanded.has(ins.name)
                const hasDetail = (ins.models?.length ?? 0) > 0 || (ins.nodes?.length ?? 0) > 0
                const of = insOkFail[ins.name]
                const ok = of?.ok ?? 0
                const fail = of?.fail ?? 0
                return (
                  <Fragment key={ins.name}>
                    <tr
                      onClick={() => hasDetail && toggleExpand(ins.name)}
                      className={clsx(
                        'border-b border-zinc-50 hover:bg-zinc-50/60 transition-colors',
                        hasDetail ? 'cursor-pointer' : '',
                      )}
                    >
                      <td className="py-2.5 pr-3 font-medium text-zinc-800 flex items-center gap-1.5">
                        {hasDetail ? (
                          open ? (
                            <ChevronDown size={14} className="text-zinc-400" />
                          ) : (
                            <ChevronRight size={14} className="text-zinc-400" />
                          )
                        ) : (
                          <span className="w-3.5" />
                        )}
                        {ins.name}
                      </td>
                      <td className="py-2.5 pr-3 text-right tabular-nums text-zinc-600">{fmt(ins.requests)}</td>
                      <td className="py-2.5 pr-3">
                        <MiniBar ok={ok} fail={fail} />
                      </td>
                      <td className="py-2.5 pr-3 text-right tabular-nums text-zinc-600">{fmt(ins.prompt_tokens)}</td>
                      <td className="py-2.5 pr-3 text-right tabular-nums text-zinc-600">{fmt(ins.completion_tokens)}</td>
                      <td className="py-2.5 pr-3 text-right tabular-nums font-medium text-zinc-900">{fmt(ins.total_tokens)}</td>
                      <td className="py-2.5">
                        {ins.exists ? (
                          <span className="inline-flex px-2 py-0.5 rounded-md bg-green-50 text-green-700 text-[11px] font-medium">
                            正常
                          </span>
                        ) : (
                          <span className="inline-flex px-2 py-0.5 rounded-md bg-zinc-100 text-zinc-500 text-[11px] font-medium">
                            已删除
                          </span>
                        )}
                      </td>
                    </tr>
                    {open && (
                      <tr key={`${ins.name}-detail`} className="bg-zinc-50/50">
                        <td colSpan={7} className="py-2 px-4">
                          <table className="w-full text-[12px]">
                            <thead>
                              <tr className="text-left text-zinc-400">
                                <th className="py-1.5 pr-3 font-medium">模型</th>
                                <th className="py-1.5 pr-3 font-medium text-right">请求数</th>
                                <th className="py-1.5 pr-3 font-medium text-right">输入</th>
                                <th className="py-1.5 pr-3 font-medium text-right">输出</th>
                                <th className="py-1.5 font-medium text-right">总计</th>
                              </tr>
                            </thead>
                            <tbody>
                              {ins.models.map((m) => (
                                <tr key={m.model} className="border-b border-zinc-100/60">
                                  <td className="py-1.5 pr-3 text-zinc-700">{m.model}</td>
                                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-500">{fmt(m.requests)}</td>
                                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-500">{fmt(m.prompt_tokens)}</td>
                                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-500">{fmt(m.completion_tokens)}</td>
                                  <td className="py-1.5 text-right tabular-nums font-medium text-zinc-700">{fmt(m.total_tokens)}</td>
                                </tr>
                              ))}
                            </tbody>
                          </table>

                          {ins.nodes && ins.nodes.length > 0 && (
                            <>
                              <div className="mt-3 mb-1 text-[12px] font-medium text-zinc-500">
                                调用节点明细（经统一网关路由）
                              </div>
                              <table className="w-full text-[12px]">
                                <thead>
                                  <tr className="text-left text-zinc-400">
                                    <th className="py-1.5 pr-3 font-medium">节点</th>
                                    <th className="py-1.5 pr-3 font-medium text-right">请求数</th>
                                    <th className="py-1.5 pr-3 font-medium text-right">输入</th>
                                    <th className="py-1.5 pr-3 font-medium text-right">输出</th>
                                    <th className="py-1.5 font-medium text-right">总计</th>
                                  </tr>
                                </thead>
                                <tbody>
                                  {ins.nodes.map((n) => (
                                    <tr key={n.addr} className="border-b border-zinc-100/60">
                                      <td className="py-1.5 pr-3 text-zinc-700">{n.name}</td>
                                      <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-500">{fmt(n.requests)}</td>
                                      <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-500">{fmt(n.prompt_tokens)}</td>
                                      <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-500">{fmt(n.completion_tokens)}</td>
                                      <td className="py-1.5 text-right tabular-nums font-medium text-zinc-700">{fmt(n.total_tokens)}</td>
                                    </tr>
                                  ))}
                                </tbody>
                              </table>
                            </>
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
        )}
        {!isEmpty && (
          <div className="mt-3 text-[11px] text-zinc-400">
            每 5 秒自动刷新 · 已删除实例的统计仍保留在历史区
          </div>
        )}
          </>
        )}
      </div>

      {/* 重置统计：二次确认弹窗 */}
      {showResetConfirm && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-900/40"
          onClick={() => setShowResetConfirm(false)}
        >
          <div
            className="bg-white rounded-2xl shadow-xl w-[420px] p-6"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="text-[15px] font-semibold text-zinc-900 mb-2">重置 Token 统计</div>
            <p className="text-[13px] text-zinc-600 leading-relaxed">
              此操作将清空所有实例与统一网关的 Token 用量数据。
            </p>
            <label className="flex items-center gap-2 mt-4 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={clearDeleted}
                onChange={(e) => setClearDeleted(e.target.checked)}
                className="accent-teal-600"
              />
              <span className="text-[13px] text-zinc-700">清除已删除节点（若存在）</span>
            </label>
            <div className="flex gap-3 mt-5 justify-end">
              <button
                type="button"
                onClick={() => setShowResetConfirm(false)}
                disabled={resetting}
                className="px-4 py-2 rounded-lg text-[13px] text-zinc-600 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-40 transition-colors"
              >
                取消
              </button>
              <button
                type="button"
                onClick={() => void doReset(clearDeleted)}
                disabled={resetting}
                className="px-4 py-2 rounded-lg text-[13px] font-medium text-white bg-red-600 hover:bg-red-700 disabled:opacity-40 transition-colors"
              >
                {resetting ? '重置中…' : '确定'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
