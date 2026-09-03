import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Activity,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Filter,
  Inbox,
  Loader2,
  RefreshCw,
  ScrollText,
  Trash2,
} from 'lucide-react'
import { api, type CallLogRecord } from '../lib/api'

const fmtTime = (ts: string) => {
  try {
    const d = new Date(ts)
    return d.toLocaleString('zh-CN', { hour12: false })
  } catch {
    return ts
  }
}

const fmtDur = (ms?: number) => {
  if (!ms || ms <= 0) return '-'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/** 日志列表前端分页：每页条数 */
const PAGE_SIZE = 100

/** 是否有需要展开详细展示的事件（异常/切换） */
const hasIssue = (rec: CallLogRecord) => {
  if (rec.status !== 'ok') return true
  return (rec.events ?? []).some((e) =>
    ['switch', 'ttft_timeout', 'silence_timeout', 'stream_interrupt', 'stream_error', 'connect_error', 'upstream_error', 'all_failed'].includes(e.type),
  )
}

/** 429/额度类错误关键字（命中 err_msg 或事件 detail → 「额度用尽」标签） */
const isRateLimited = (rec: CallLogRecord) => {
  const hay = [rec.err_msg || '', ...(rec.events ?? []).map((e) => e.detail || '')]
    .join(' ')
    .toLowerCase()
  return /429|rate\s*limit|quota|insufficient|exceeded|额度|配额|用尽|限流/.test(hay)
}

const issueLabel = (rec: CallLogRecord): string => {
  const ev = rec.events ?? []
  if (ev.some((e) => e.type === 'all_failed')) return '全部节点失败'
  if (ev.some((e) => e.type === 'switch')) return '已切换节点'
  if (ev.some((e) => e.type === 'ttft_timeout')) return '首字超时'
  if (ev.some((e) => e.type === 'silence_timeout')) return '静默超时'
  if (ev.some((e) => e.type === 'stream_interrupt')) return '流中断'
  if (ev.some((e) => e.type === 'stream_error')) return '流错误'
  if (ev.some((e) => e.type === 'connect_error')) return '连接失败'
  if (ev.some((e) => e.type === 'upstream_error')) return '上游错误'
  return '异常'
}

/** 行级异常标签配色：失败类红 / 超时类玫红 / 切换与其它琥珀 */
const issueTagClass = (rec: CallLogRecord): string => {
  const ev = rec.events ?? []
  if (ev.some((e) => ['all_failed', 'stream_error', 'connect_error', 'upstream_error'].includes(e.type))) {
    return 'text-red-700 bg-red-100'
  }
  if (ev.some((e) => ['ttft_timeout', 'silence_timeout', 'stream_interrupt'].includes(e.type))) {
    return 'text-rose-700 bg-rose-100'
  }
  return 'text-amber-700 bg-amber-100'
}

/** 行内完整错误文本（err_msg + 事件 detail 拼接，供 title 悬停展示全文） */
const rowErrText = (rec: CallLogRecord): string | undefined => {
  const parts = [rec.err_msg || '', ...(rec.events ?? []).map((e) => e.detail || '')].filter(Boolean)
  return parts.length ? parts.join(' · ') : undefined
}

/** 时间线事件配色：切换琥珀 / 超时类玫红 / 失败类红 / 成功绿 / 其它灰 */
const eventClass = (type: string): string => {
  switch (type) {
    case 'switch':
      return 'bg-amber-100 text-amber-800'
    case 'ttft_timeout':
    case 'silence_timeout':
    case 'stream_interrupt':
      return 'bg-rose-100 text-rose-700'
    case 'all_failed':
    case 'stream_error':
    case 'connect_error':
    case 'upstream_error':
      return 'bg-red-100 text-red-700'
    case 'connect_ok':
    case 'complete':
      return 'bg-green-100 text-green-700'
    default:
      return 'bg-zinc-200 text-zinc-700'
  }
}

/**
 * 路由判定：后端在调用日志上写入的 route_verdict 字符串枚举（omitempty）。
 * 前端只做「枚举 → 展现」映射，不再靠 nodes[0] === '直连' 比对中文字面量猜语义，
 * 也不依赖从未被写入的死字段 via_proxy。
 * 旧记录不含该键 → routeVerdictTag 返回 null → UI 降级为 '-'（不报错、不上告警色）。
 */
type LogRecord = CallLogRecord & { route_verdict?: string }

type RouteVerdictTag = { label: string; color: string }

/** route_verdict 枚举 → 标签文案与配色（沿用本项目小标签风格：bg-*-50/100 + text-*-700） */
const ROUTE_VERDICT_TAGS: Record<string, RouteVerdictTag> = {
  // 真实走了代理节点：中性标签，不加告警色
  proxied: { label: '走代理节点', color: 'bg-zinc-100 text-zinc-600' },
  // 付费层恒直连，设计如此
  direct_by_design: { label: '设计直连（付费层）', color: 'bg-green-50 text-green-700' },
  // 免费层应走代理但 SOCKS 未配置 → 事故，红色告警
  direct_config_missing: { label: '配置丢失·已回退直连', color: 'bg-red-100 text-red-700' },
  // 免费层、SOCKS 有配置但节点仍空 → 灰色异常
  direct_unexpected: { label: '异常直连', color: 'bg-zinc-200 text-zinc-700' },
}

/** 取路由判定标签；空值 / 缺失 / 未知枚举 → null（调用方降级为 '-'） */
const routeVerdictTag = (rec: LogRecord): RouteVerdictTag | null => {
  const v = rec.route_verdict
  if (!v) return null
  return ROUTE_VERDICT_TAGS[v] ?? null
}

/** 单条日志行（memo 化：展开/翻页/筛选切换时只重渲染变化的行） */
const LogRow = memo(function LogRow({
  rec,
  isExpanded,
  onToggle,
}: {
  rec: LogRecord
  isExpanded: boolean
  onToggle: (id: string) => void
}) {
  const issue = hasIssue(rec)
  const nodes = rec.nodes ?? []
  const verdict = routeVerdictTag(rec)
  return (
    <div
      className={clsx(
        'rounded-2xl border bg-white overflow-hidden',
        issue ? 'border-amber-300/70' : 'border-zinc-200',
      )}
    >
      {/* 成功：一行简短；异常：可展开 */}
      <button
        type="button"
        onClick={() => issue && onToggle(rec.req_id)}
        title={issue ? rowErrText(rec) : undefined}
        className={clsx(
          'w-full flex items-center gap-3 px-4 py-2.5 text-left',
          issue && 'cursor-pointer hover:bg-zinc-50',
        )}
      >
        <span
          className={clsx(
            'shrink-0 w-[68px] text-center text-xs font-medium rounded-md py-0.5',
            rec.status === 'ok' ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-700',
          )}
        >
          {rec.status === 'ok' ? '【成功】' : '【失败】'}
        </span>
        {isRateLimited(rec) && (
          <span className="shrink-0 text-[11px] text-zinc-50 bg-zinc-800 rounded-md px-2 py-0.5">
            额度用尽
          </span>
        )}
        {rec.route_verdict === 'direct_config_missing' && verdict && (
          <span
            className="shrink-0 text-[11px] rounded-md px-2 py-0.5 bg-red-100 text-red-700"
            title="免费层本应走代理节点，但 SOCKS 未配置，本次已回退直连"
          >
            {verdict.label}
          </span>
        )}
        {issue && (
          <span className={clsx('shrink-0 text-[11px] rounded-md px-2 py-0.5', issueTagClass(rec))}>
            {issueLabel(rec)}
          </span>
        )}
        {rec.source && (
          <span className="shrink-0 text-[11px] text-indigo-700 bg-indigo-100 rounded-md px-2 py-0.5">
            {rec.source}
          </span>
        )}
        <span className="text-zinc-500 text-xs tabular-nums shrink-0">{fmtTime(rec.ts)}</span>
        <span className="text-zinc-800 text-sm font-medium truncate flex-1">
          {rec.model || '-'}
        </span>
        {nodes.length > 0 && (
          <span className="text-zinc-500 text-xs truncate hidden sm:inline">
            {nodes.join(' → ')}
          </span>
        )}
        <span className="text-zinc-400 text-xs tabular-nums shrink-0">
          {fmtDur(rec.duration_ms)}
        </span>
        {issue && (
          <span className="shrink-0 text-zinc-400">
            {isExpanded ? <ChevronUpIcon /> : <ChevronRight size={16} />}
          </span>
        )}
      </button>

      {/* 异常/切换：整块详细时间线 */}
      {issue && isExpanded && (
        <div className="border-t border-zinc-100 px-4 py-3 bg-zinc-50/60">
          <div className="text-xs text-zinc-500 mb-2 font-mono break-all">
            req_id: {rec.req_id} · {rec.path || '/v1/chat/completions'} · stream: {rec.stream ? '是' : '否'} · 路由: {rec.route_mode || '-'} · 层: {rec.tier || '-'} · 路由判定:{' '}
            {verdict ? (
              <span className={clsx('text-[11px] px-1.5 py-0.5 rounded font-sans align-middle', verdict.color)}>
                {verdict.label}
              </span>
            ) : (
              '-'
            )}
            {rec.serving_port ? ` · 端口: ${rec.serving_port}` : ''}
            {rec.source && <span className="text-indigo-600"> · 来源: {rec.source}</span>}
            {rec.err_msg && <span className="text-red-600" title={rec.err_msg}> · 错误: {rec.err_msg}</span>}
          </div>
          <div className="text-xs text-zinc-500 mb-2">
            token: 输入 {rec.prompt_tokens ?? 0} / 输出 {rec.completion_tokens ?? 0} · 耗时 {fmtDur(rec.duration_ms)}
          </div>
          <div className="space-y-1.5">
            {(rec.events ?? []).map((ev, i) => (
              <div key={i} className="flex items-start gap-2 text-xs">
                <span className="shrink-0 w-24 text-zinc-400 tabular-nums">{fmtTime(ev.at ?? '')}</span>
                <span
                  className={clsx(
                    'shrink-0 w-28 rounded px-1.5 py-0.5 text-center font-medium',
                    eventClass(ev.type ?? ''),
                  )}
                >
                  {ev.type}
                </span>
                <span className="text-zinc-700 break-all" title={[ev.node, ev.detail].filter(Boolean).join(' — ')}>
                  {ev.node && <b className="text-zinc-900">{ev.node}</b>}
                  {ev.node && ev.detail ? ' — ' : ''}
                  {ev.detail}
                </span>
              </div>
            ))}
            {rec.events?.length === 0 && (
              <span className="text-zinc-400">无事件明细</span>
            )}
          </div>
        </div>
      )}
    </div>
  )
})

export default function LogsPage({
  toast,
}: {
  toast: (msg: string, ok?: boolean) => void
}) {
  const [logs, setLogs] = useState<LogRecord[]>([])
  const [error, setError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  // P2 audit: 清空日志忙态（spinner + 禁用）
  const [clearing, setClearing] = useState(false)
  const [onlyIssues, setOnlyIssues] = useState(false)
  // 按天筛选：'' = 全部日期
  const [dateFilter, setDateFilter] = useState('')
  // 视图切换：日志列表 / 时段分析 / 节点分析
  const [view, setView] = useState<'list' | 'hour' | 'node'>('list')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  // 关键词过滤（main 功能 M4：匹配 model/path/err_msg/req_id）
  const [keyword, setKeyword] = useState('')
  // 前端分页：最新在前，第 1 页 = 最新一页
  const [page, setPage] = useState(1)

  // 拉取量跟随 call_log_max（设置页可调）：上限保持 5000；call_log_max<=0 或读配置失败回退默认 5000
  const fetchLimitRef = useRef(5000)
  useEffect(() => {
    api
      .configGet()
      .then((c) => {
        fetchLimitRef.current = c.call_log_max > 0 ? Math.min(c.call_log_max, 5000) : 5000
      })
      .catch(() => {})
  }, [])

  // G31: toast 用 ref 封装（App 的 showToast 每次渲染重建）——load 依赖不含 toast，轮询定时器不因 toast 重启
  const toastRef = useRef(toast)
  toastRef.current = toast

  // M9: 轮询代次守卫——load 开始记代，响应后比对，过期响应丢弃（慢响应不叠加、旧快照不覆盖新状态）
  const loadGen = useRef(0)
  const load = useCallback(
    async (silent = true) => {
      const gen = ++loadGen.current
      try {
        const recs = await api.getCallLog(fetchLimitRef.current)
        if (gen !== loadGen.current) return
        // 最新在前（后端无日志时可能返回 null → 归一为空数组，避免 TypeError）
        setLogs([...(Array.isArray(recs) ? recs : [])].reverse())
        setError(null)
      } catch (e) {
        if (gen !== loadGen.current) return
        if (!silent) toastRef.current(String(e), false)
        else setError(String(e))
      }
    },
    [],
  )

  // 自动轮询（静默，5s）
  useEffect(() => {
    void load()
    const t = setInterval(() => void load(true), 5000)
    return () => clearInterval(t)
  }, [load])

  const doRefresh = async () => {
    setRefreshing(true)
    await load(false)
    setRefreshing(false)
  }

  // P2 audit: 清空全部调用日志（异步 → 忙态 spinner + 禁用）
  const doClearLogs = async () => {
    if (clearing) return
    if (!confirm('确定清空全部调用日志？该操作不可恢复。')) return
    setClearing(true)
    try {
      await api.clearCallLog()
      // M9: 作废在途轮询响应——清空后旧日志不再被慢响应回填
      loadGen.current++
      setLogs([])
      setPage(1)
      toast('日志已清空')
    } catch (e) {
      toast(String(e), false)
    } finally {
      setClearing(false)
    }
  }

  const toggleExpand = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // 日志中出现的日期（YYYY-MM-DD，新→旧），供按天筛选
  const dates = useMemo(() => {
    const s = new Set<string>()
    for (const l of logs) {
      const d = (l.ts || '').slice(0, 10)
      if (d) s.add(d)
    }
    return [...s].sort().reverse()
  }, [logs])

  // G27: 轮询替换日志后 dates 可能收窄——过期的按天筛选自动复位（防止列表被看不见的日期过滤为空）
  useEffect(() => {
    if (dateFilter && !dates.includes(dateFilter)) setDateFilter('')
  }, [dates, dateFilter])

  const visible = useMemo(() => {
    return logs.filter((l) => {
      if (onlyIssues && !hasIssue(l)) return false
      if (dateFilter && (l.ts || '').slice(0, 10) !== dateFilter) return false
      if (keyword) {
        const hay = [l.model || '', l.path || '', l.err_msg || '', l.req_id || ''].join(' ')
        if (!hay.includes(keyword)) return false
      }
      return true
    })
  }, [logs, onlyIssues, dateFilter, keyword])

  // 前端分页：每页 100 条；页码 clamp 到有效范围（清空/删除/筛选收窄后安全复位，不跳页）
  const totalPages = Math.max(1, Math.ceil(visible.length / PAGE_SIZE))
  const currentPage = Math.min(page, totalPages)
  const pageItems = useMemo(() => {
    const start = (currentPage - 1) * PAGE_SIZE
    return visible.slice(start, start + PAGE_SIZE)
  }, [visible, currentPage])

  // 汇总统计与列表同一筛选视图（日期/关键词/只看失败均联动）。
  // 三类互斥：失败（最终失败）/ 异常切换（成功但中途切换/超时/中断等事件）/ 成功（无事件）——相加恰为总数。
  const failCount = visible.filter((l) => l.status !== 'ok').length
  const issueCount = visible.filter((l) => l.status === 'ok' && hasIssue(l)).length
  const okCount = visible.length - failCount - issueCount

  return (
    <div className="p-6">
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-[16px] font-semibold text-zinc-900 flex items-center gap-2.5">
          <ScrollText size={18} className="text-teal-700" />
          调用日志
        </h1>
        <div className="flex items-center gap-2">
          <button
            onClick={() => void doClearLogs()}
            disabled={logs.length === 0 || clearing}
            className="flex items-center gap-1.5 bg-white border border-zinc-200 text-zinc-600 rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-50 disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {clearing ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
            {clearing ? '清空中…' : '清空'}
          </button>
          <button
            onClick={doRefresh}
            disabled={refreshing}
            className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-700 disabled:opacity-50"
          >
            <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
            刷新
          </button>
        </div>
      </div>

      {/* 过滤工具条：只看失败 / 按天 / 关键词（main 功能 M4） */}
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <input
          type="text"
          placeholder="关键词过滤（模型/路径/错误/请求ID）"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          className="flex-1 min-w-[200px] px-3 py-2 border rounded-lg text-sm"
        />
      </div>

      {/* 视图切换：日志列表 / 时段分析 / 节点分析 */}
      <div className="flex items-center gap-1 mb-4 bg-zinc-100/80 rounded-xl p-1 w-fit">
        {(
          [
            ['list', '日志列表'],
            ['hour', '时段分析'],
            ['node', '节点分析'],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            type="button"
            onClick={() => setView(id)}
            className={clsx(
              'px-4 py-1.5 rounded-lg text-[13px] font-medium transition-colors',
              view === id ? 'bg-white text-zinc-900 shadow-sm' : 'text-zinc-500 hover:text-zinc-700',
            )}
          >
            {label}
          </button>
        ))}
      </div>

      {/* 汇总 + 过滤 */}
      {view === 'list' && (
      <>
      <div className="bg-white rounded-2xl border p-4 mb-4 flex flex-wrap items-center gap-4">
        <div className="flex gap-5 text-sm">
          <span className="text-zinc-600">
            共 <b className="text-zinc-900">{visible.length}</b> 条
          </span>
          <span className="text-green-600">
            【成功】<b>{okCount}</b>
          </span>
          <span className="text-red-600">
            【失败】<b>{failCount}</b>
          </span>
          <span className="text-amber-600">
            异常/切换 <b>{issueCount}</b>
          </span>
        </div>
        {dates.length > 1 && (
        <select
          value={dateFilter}
          onChange={(e) => setDateFilter(e.target.value)}
          className="px-2.5 py-1.5 rounded-lg border border-zinc-200 bg-white text-[13px] text-zinc-600 outline-none"
          title="按日期筛选"
        >
          <option value="">全部日期（{dates.length} 天）</option>
          {dates.map((d) => (
            <option key={d} value={d}>
              {d}
            </option>
          ))}
        </select>
        )}
        <label className="flex items-center gap-2 text-sm text-zinc-600 cursor-pointer ml-auto">
          <input
            type="checkbox"
            checked={onlyIssues}
            onChange={(e) => setOnlyIssues(e.target.checked)}
            className="accent-zinc-900"
          />
          <Filter size={14} />
          只看失败/切换
        </label>
      </div>

      {error && <div className="text-red-600 text-sm mb-4">加载失败：{error}</div>}

      {/* 日志列表 */}
      {visible.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-zinc-400">
          <Inbox size={40} strokeWidth={1.5} />
          <p className="mt-3 text-sm">暂无日志</p>
          <p className="text-xs mt-1">
            {logs.length === 0 ? '网关/独享实例尚未记录调用（需以网关或独享实例模式运行）' : '没有匹配的日志'}
          </p>
        </div>
      ) : (
        <>
          <div className="space-y-2">
            {pageItems.map((rec) => (
              <LogRow
                key={rec.req_id}
                rec={rec}
                isExpanded={expanded.has(rec.req_id)}
                onToggle={toggleExpand}
              />
            ))}
          </div>
          {/* 分页条：上一页/下一页 + 页码（最后一页可能不满一页） */}
          <div className="flex items-center justify-center gap-4 mt-4">
            <button
              type="button"
              onClick={() => setPage(Math.max(1, currentPage - 1))}
              disabled={currentPage <= 1}
              className="flex items-center gap-1 bg-white border border-zinc-200 text-zinc-600 rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-50 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <ChevronLeft size={14} />
              上一页
            </button>
            <span className="text-sm text-zinc-500 tabular-nums">
              第 {currentPage} / {totalPages} 页
            </span>
            <button
              type="button"
              onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
              disabled={currentPage >= totalPages}
              className="flex items-center gap-1 bg-white border border-zinc-200 text-zinc-600 rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-50 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              下一页
              <ChevronRight size={14} />
            </button>
          </div>
        </>
      )}

      {/* 空态提示 */}
      {logs.length > 0 && visible.length === 0 && null}
      <div className="flex items-center gap-2 text-zinc-400 text-xs mt-4">
        <Activity size={12} />
        每 5 秒自动刷新 · 保留上限可在设置页调整
      </div>
      </>
      )}

      {/* 时段分析（阶段4填充） */}
      {view === 'hour' && <HourAnalysisView logs={logs} />}
      {/* 节点分析（阶段4填充） */}
      {view === 'node' && <NodeAnalysisView logs={logs} />}
    </div>
  )
}

/** 超时/错误类事件（用于分析统计） */
const ISSUE_EVENT_TYPES = [
  'switch',
  'ttft_timeout',
  'silence_timeout',
  'stream_interrupt',
  'stream_error',
  'connect_error',
  'upstream_error',
  'all_failed',
]

type HourStat = {
  hour: number
  requests: number
  ok: number
  totalMs: number
  issueCount: number
}

type NodeStat = {
  node: string
  requests: number
  ok: number
  totalMs: number
  issueCount: number
}

/** 层分组键：free / paid / unknown（旧记录 tier 为空，单列一组，不静默并入 free 或 paid） */
type TierKey = 'free' | 'paid' | 'unknown'

type TierStat = {
  tier: TierKey
  requests: number
  /** 最终失败（复用本页失败判定：status !== 'ok'） */
  failed: number
  /** 成功但中途有异常/切换事件（复用 hasIssue） */
  issueCount: number
  totalMs: number
  /** route_verdict === 'direct_config_missing' 的次数（配置丢失回退直连） */
  configMissing: number
}

const TIER_LABELS: Record<TierKey, string> = {
  free: 'free · 免费层（通常走代理池）',
  paid: 'paid · 付费层（恒直连）',
  unknown: '未知（旧记录无 tier）',
}

/** tier 归一化：只有 'free' / 'paid' 有效，其余（空/缺失/未知值）归入 unknown */
const tierKey = (t?: string): TierKey => (t === 'free' ? 'free' : t === 'paid' ? 'paid' : 'unknown')

const fmtPct = (n: number) => `${(n * 100).toFixed(0)}%`

/** 时段分析视图：按小时聚合请求数/平均耗时/失败率/异常次数（纯 CSS 条形图） */
function HourAnalysisView({ logs }: { logs: CallLogRecord[] }) {
  const hours = useMemo(() => {
    const arr: HourStat[] = Array.from({ length: 24 }, (_, i) => ({
      hour: i,
      requests: 0,
      ok: 0,
      totalMs: 0,
      issueCount: 0,
    }))
    for (const l of logs) {
      const d = new Date(l.ts)
      if (Number.isNaN(d.getTime())) continue
      const s = arr[d.getHours()]!
      s.requests++
      if (l.status === 'ok') s.ok++
      s.totalMs += l.duration_ms ?? 0
      s.issueCount += (l.events ?? []).filter((e) => ISSUE_EVENT_TYPES.includes(e.type)).length
    }
    return arr
  }, [logs])

  const withData = hours.filter((h) => h.requests > 0)
  const maxReq = Math.max(1, ...hours.map((h) => h.requests))

  return (
    <div className="bg-white rounded-2xl border border-zinc-200 p-5">
      <div className="text-[14px] font-semibold text-zinc-900 mb-1">时段分析</div>
      <div className="text-[12px] text-zinc-400 mb-4">
        按小时统计请求分布与耗时，帮助定位一天中相对卡顿的时段（数据来自保留期内的调用日志）
      </div>
      {withData.length === 0 ? (
        <div className="py-12 text-center text-zinc-400 text-sm">暂无日志数据</div>
      ) : (
        <>
          {/* 24 小时条形图（柱高 ∝ 请求数） */}
          <div className="flex items-end gap-[3px] h-32 mb-4">
            {hours.map((h) => (
              <div key={h.hour} className="flex-1 flex flex-col justify-end items-center h-full group relative" title={`${String(h.hour).padStart(2, '0')} 时：${h.requests} 请求`}>
                {h.requests > 0 && (
                  <>
                    <div
                      className={clsx(
                        'w-full rounded-t-sm transition-all',
                        h.requests > 0 && h.ok / h.requests >= 0.9 ? 'bg-teal-500' : 'bg-amber-400',
                      )}
                      style={{ height: `${Math.max((h.requests / maxReq) * 100, 4)}%` }}
                    />
                    <div className="absolute bottom-full mb-1 hidden group-hover:block bg-zinc-900 text-white text-[10px] rounded px-1.5 py-0.5 whitespace-nowrap z-10">
                      {String(h.hour).padStart(2, '0')} 时 · {h.requests} 请求 · 均耗 {fmtDur(h.requests ? Math.round(h.totalMs / h.requests) : 0)}
                    </div>
                  </>
                )}
              </div>
            ))}
          </div>
          {/* 明细表 */}
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
                <th className="py-2 pr-3 font-medium">时段</th>
                <th className="py-2 pr-3 font-medium text-right">请求数</th>
                <th className="py-2 pr-3 font-medium text-right">平均耗时</th>
                <th className="py-2 pr-3 font-medium text-right">失败率</th>
                <th className="py-2 font-medium text-right">异常/切换</th>
              </tr>
            </thead>
            <tbody>
              {withData.map((h) => (
                <tr key={h.hour} className="border-b border-zinc-50">
                  <td className="py-1.5 pr-3 text-zinc-800">{String(h.hour).padStart(2, '0')}:00 - {String(h.hour).padStart(2, '0')}:59</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-600">{h.requests}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-600">{fmtDur(Math.round(h.totalMs / h.requests))}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums text-zinc-600">{fmtPct(1 - h.ok / h.requests)}</td>
                  <td className="py-1.5 text-right tabular-nums text-zinc-600">{h.issueCount}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}

/** 节点分析视图：按节点聚合请求数/成功率/平均耗时/异常次数（排序表 + 成功率条） */
function NodeAnalysisView({ logs }: { logs: LogRecord[] }) {
  // 按 tier 分组统计（free / paid / 未知）：请求数、失败数、异常切换、平均耗时、配置丢失回退次数
  const tiers = useMemo(() => {
    const order: TierKey[] = ['free', 'paid', 'unknown']
    const m = new Map<TierKey, TierStat>(
      order.map((k) => [
        k,
        { tier: k, requests: 0, failed: 0, issueCount: 0, totalMs: 0, configMissing: 0 },
      ]),
    )
    for (const l of logs) {
      const s = m.get(tierKey(l.tier))!
      s.requests++
      if (l.status !== 'ok') s.failed++
      else if (hasIssue(l)) s.issueCount++
      s.totalMs += l.duration_ms ?? 0
      if (l.route_verdict === 'direct_config_missing') s.configMissing++
    }
    return order.map((k) => m.get(k)!).filter((s) => s.requests > 0)
  }, [logs])

  const nodes = useMemo(() => {
    const m = new Map<string, NodeStat>()
    for (const l of logs) {
      // 最终节点：nodes 链最后一项；无则归「未知」
      const node = l.nodes?.slice(-1)[0] ?? '未知'
      let s = m.get(node)
      if (!s) {
        s = { node, requests: 0, ok: 0, totalMs: 0, issueCount: 0 }
        m.set(node, s)
      }
      s.requests++
      if (l.status === 'ok') s.ok++
      s.totalMs += l.duration_ms ?? 0
      s.issueCount += (l.events ?? []).filter((e) => ISSUE_EVENT_TYPES.includes(e.type)).length
    }
    return [...m.values()].sort((a, b) => b.requests - a.requests || b.issueCount - a.issueCount)
  }, [logs])

  return (
    <div className="space-y-4">
      {/* 按层分组统计：free / paid / 未知（空 tier 不并入任何有效层，避免统计说谎） */}
      <div className="bg-white rounded-2xl border border-zinc-200 p-5">
        <div className="text-[14px] font-semibold text-zinc-900 mb-1">按层分组</div>
        <div className="text-[12px] text-zinc-400 mb-4">
          按 tier 分组：免费层通常走代理池，付费层恒直连；旧记录无 tier 字段，单列「未知」组
        </div>
        {tiers.length === 0 ? (
          <div className="py-8 text-center text-zinc-400 text-sm">暂无日志数据</div>
        ) : (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
                <th className="py-2 pr-3 font-medium">层</th>
                <th className="py-2 pr-3 font-medium text-right">请求数</th>
                <th className="py-2 pr-3 font-medium text-right">失败数</th>
                <th className="py-2 pr-3 font-medium text-right">失败率</th>
                <th className="py-2 pr-3 font-medium text-right">异常/切换</th>
                <th className="py-2 pr-3 font-medium text-right">平均耗时</th>
                <th className="py-2 font-medium text-right">配置丢失回退</th>
              </tr>
            </thead>
            <tbody>
              {tiers.map((t) => (
                <tr key={t.tier} className="border-b border-zinc-50">
                  <td className="py-2 pr-3 text-zinc-800">{TIER_LABELS[t.tier]}</td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{t.requests}</td>
                  <td className={clsx('py-2 pr-3 text-right tabular-nums', t.failed > 0 ? 'text-red-600' : 'text-zinc-600')}>
                    {t.failed}
                  </td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">
                    {fmtPct(t.requests > 0 ? t.failed / t.requests : 0)}
                  </td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{t.issueCount}</td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">
                    {fmtDur(t.requests > 0 ? Math.round(t.totalMs / t.requests) : 0)}
                  </td>
                  <td className={clsx('py-2 text-right tabular-nums', t.configMissing > 0 ? 'text-red-600' : 'text-zinc-600')}>
                    {t.configMissing}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* 节点维度（原有表格） */}
      <div className="bg-white rounded-2xl border border-zinc-200 p-5">
      <div className="text-[14px] font-semibold text-zinc-900 mb-1">节点分析</div>
      <div className="text-[12px] text-zinc-400 mb-4">
        按最终出口节点聚合，评估各节点请求量、成功率与稳定性（数据来自保留期内的调用日志）
      </div>
      {nodes.length === 0 ? (
        <div className="py-12 text-center text-zinc-400 text-sm">暂无日志数据</div>
      ) : (
        <table className="w-full text-[13px]">
          <thead>
            <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
              <th className="py-2 pr-3 font-medium">节点</th>
              <th className="py-2 pr-3 font-medium text-right">请求数</th>
              <th className="py-2 pr-3 font-medium text-right">成功率</th>
              <th className="py-2 pr-3 font-medium text-right">平均耗时</th>
              <th className="py-2 font-medium text-right">异常/切换</th>
            </tr>
          </thead>
          <tbody>
            {nodes.map((n) => {
              const rate = n.requests > 0 ? n.ok / n.requests : 0
              return (
                <tr key={n.node} className="border-b border-zinc-50">
                  <td className="py-2 pr-3">
                    <div className="flex items-center gap-2">
                      <span className="text-zinc-800 font-mono text-[12px] truncate max-w-[220px]">{n.node}</span>
                      <div className="flex-1 h-1.5 bg-zinc-100 rounded-full overflow-hidden min-w-[60px]">
                        <div
                          className={clsx('h-full rounded-full', rate >= 0.9 ? 'bg-teal-500' : rate >= 0.5 ? 'bg-amber-400' : 'bg-red-400')}
                          style={{ width: `${rate * 100}%` }}
                        />
                      </div>
                    </div>
                  </td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{n.requests}</td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmtPct(rate)}</td>
                  <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{fmtDur(Math.round(n.totalMs / n.requests))}</td>
                  <td className="py-2 text-right tabular-nums text-zinc-600">{n.issueCount}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      </div>
    </div>
  )
}

function ChevronUpIcon() {
  return <ChevronDown size={16} className="rotate-180" />
}
