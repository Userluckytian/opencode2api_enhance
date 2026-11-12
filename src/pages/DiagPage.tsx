import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  Copy,
  Loader2,
  RefreshCw,
  Stethoscope,
  XCircle,
  Zap,
  BarChart3,
  Lightbulb,
  KeyRound,
} from 'lucide-react'
import {
  api,
  type DiagReport,
  type FailAggregate,
  type FreeTierEngineStatus,
  type ModelProbeResult,
  type MetricSummary,
  type MetricMatrix,
  type MetricKeyStats,
  type MetricSuggestions,
} from '../lib/api'
import ChannelStatsCard from './ChannelStatsCard'

/** 状态码 → 颜色：2xx 绿、4xx 橙、5xx 红。 */
function statusCls(code: number): string {
  if (code >= 200 && code < 300) return 'bg-green-50 text-green-700'
  if (code >= 400 && code < 500) return 'bg-amber-50 text-amber-700'
  if (code >= 500) return 'bg-red-50 text-red-600'
  return 'bg-zinc-100 text-zinc-500'
}

/** 相对时间。 */
function fmtRel(ts?: string): string {
  if (!ts) return '—'
  const t = Date.parse(ts)
  if (!Number.isFinite(t)) return '—'
  const diff = Date.now() - t
  if (diff < 0) return '刚刚'
  const min = Math.floor(diff / 60000)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} 小时前`
  return `${Math.floor(hr / 24)} 天前`
}

export default function DiagPage({
  toast,
}: {
  toast: (msg: string, ok?: boolean) => void
}) {
  const [diag, setDiag] = useState<DiagReport | null>(null)
  const [engine, setEngine] = useState<FreeTierEngineStatus | null>(null)
  const [fails, setFails] = useState<FailAggregate | null>(null)
  const [probeResults, setProbeResults] = useState<ModelProbeResult[]>([])
  const [probeNote, setProbeNote] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [probing, setProbing] = useState<string | 'all' | null>(null)
  const [probingModel, setProbingModel] = useState('')
  const [growing, setGrowing] = useState<number | null>(null)

  // 调用分析（v2.0.0 增补）：只读指标，按窗口天数各自加载
  const [metricDays, setMetricDays] = useState(7)
  const [metricSummary, setMetricSummary] = useState<MetricSummary | null>(null)
  const [metricMatrix, setMetricMatrix] = useState<MetricMatrix | null>(null)
  const [metricKeys, setMetricKeys] = useState<MetricKeyStats | null>(null)
  const [metricSuggestions, setMetricSuggestions] = useState<MetricSuggestions | null>(null)
  const [metricLoading, setMetricLoading] = useState(true)
  // 矩阵排序：点击表头切换
  const [matrixSort, setMatrixSort] = useState<'requests' | 'ok_rate' | 'avg_ms'>('requests')

  const aliveRef = useRef(true)
  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      const [d, e, f] = await Promise.all([
        api.diag().catch(() => null),
        api.freeTierEngineStatus().catch(() => null),
        api.failAggregate(2000).catch(() => null),
      ])
      if (!aliveRef.current) return
      setDiag(d)
      setEngine(e)
      setFails(f)
    } finally {
      if (aliveRef.current) setLoading(false)
    }
  }, [])

  // loadMetrics 拉四个指标视图。与 reload 分开：指标数据是磁盘现算、
  // 比自检慢，不适合跟着 15s 轮询；只在窗口切换或手动刷新时拉一次。
  const loadMetrics = useCallback(async (days: number) => {
    setMetricLoading(true)
    try {
      const [sum, mat, keys, sug] = await Promise.all([
        api.metricSummary(days).catch(() => null),
        api.metricMatrix(days).catch(() => null),
        api.metricKeys(days).catch(() => null),
        api.metricSuggestions(days).catch(() => null),
      ])
      if (!aliveRef.current) return
      setMetricSummary(sum)
      setMetricMatrix(mat)
      setMetricKeys(keys)
      setMetricSuggestions(sug)
    } finally {
      if (aliveRef.current) setMetricLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadMetrics(metricDays)
  }, [metricDays, loadMetrics])

  useEffect(() => {
    void reload()
    const t = window.setInterval(() => void reload(), 15000)
    return () => window.clearInterval(t)
  }, [reload])

  const doProbe = async (model?: string) => {
    setProbing(model ?? 'all')
    try {
      const r = await api.freeTierProbe(model)
      if (!aliveRef.current) return
      setProbeResults(r.results)
      setProbeNote(r.note ?? '')
      const okCount = r.results.filter((x) => x.ok).length
      toast(
        model
          ? `${model} → ${r.results[0]?.status ?? 0}`
          : `探测完成：${okCount}/${r.results.length} 可用`,
        okCount > 0,
      )
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      if (aliveRef.current) setProbing(null)
    }
  }

  // growPool 调整 WARP 出口池大小并重建引擎配置 + 重启。
  //
  // 为什么默认不开大：额度主要绑 session id，出口多不等于额度多；
  // 而每个出口注册要 10~90s，盲目开大会拖慢启动。所以默认 2~4，
  // 并发真的不够时在这里手动加。
  const growPool = async (n: number) => {
    setGrowing(n)
    try {
      await api.configSet('engine_warp_pool_size', String(n))
      await api.freeTierEngineWriteConfig()
      const st = await api.freeTierEngineRestart()
      if (!aliveRef.current) return
      setEngine(st)
      toast(`出口池已设为 ${n}，引擎正在重建`)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      if (aliveRef.current) setGrowing(null)
    }
  }

  const copyAll = async () => {
    const lines: string[] = []
    lines.push(`# opencode2api 诊断报告 ${diag?.generated_at ?? ''}`)
    lines.push('')
    lines.push('## 自检')
    for (const c of diag?.checks ?? []) {
      lines.push(`[${c.ok ? 'OK  ' : 'FAIL'}] ${c.name.padEnd(16)} ${c.target}  ${c.detail ?? ''}`)
    }
    if (engine) {
      lines.push('')
      lines.push('## 引擎')
      lines.push(`state=${engine.state} running=${engine.running} healthy=${engine.healthy} port=${engine.port}`)
      lines.push(`models=${engine.model_shown}/${engine.model_total} warp=${engine.warp_status ?? '-'} active=${engine.warp_active} cooling=${engine.warp_cooling}`)
    }
    if (fails && fails.buckets.length > 0) {
      lines.push('')
      lines.push('## 失败分布')
      for (const b of fails.buckets) {
        lines.push(`${b.vendor.padEnd(20)} ${b.reason.padEnd(12)} ${b.count}`)
      }
    }
    try {
      await navigator.clipboard.writeText(lines.join('\n'))
      toast('诊断报告已复制')
    } catch {
      toast('复制失败', false)
    }
  }

  const okCount = diag?.checks.filter((c) => c.ok).length ?? 0
  const totalChecks = diag?.checks.length ?? 0

  return (
    <div className="p-6 space-y-4">
      {/* 工具条 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Stethoscope size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">诊断</h1>
          {diag && (
            <span
              className={clsx(
                'px-2.5 py-1 rounded-full text-xs font-medium',
                okCount === totalChecks
                  ? 'bg-green-50 text-green-700'
                  : 'bg-amber-50 text-amber-700',
              )}
            >
              {okCount}/{totalChecks} 通过
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => void copyAll()}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
          >
            <Copy size={14} />
            复制报告
          </button>
          <button
            onClick={() => void reload()}
            disabled={loading}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-50"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
            刷新
          </button>
        </div>
      </div>

      {/* 自检表 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <CheckCircle2 size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">自检</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">{fmtRel(diag?.generated_at)}</span>
        </div>
        {!diag || diag.checks.length === 0 ? (
          <div className="text-[12px] text-zinc-400">正在检查…</div>
        ) : (
          <div className="overflow-hidden rounded-xl border border-zinc-100">
            <table className="w-full text-[13px]">
              <thead className="bg-zinc-50 text-zinc-500">
                <tr>
                  <th className="text-left font-medium px-3 py-2 w-8"></th>
                  <th className="text-left font-medium px-3 py-2 w-32">检查项</th>
                  <th className="text-left font-medium px-3 py-2">目标</th>
                  <th className="text-left font-medium px-3 py-2">详情</th>
                  <th className="text-right font-medium px-3 py-2 w-20">耗时</th>
                </tr>
              </thead>
              <tbody>
                {diag.checks.map((c) => (
                  <tr key={c.name} className="border-t border-zinc-100">
                    <td className="px-3 py-2">
                      {c.ok ? (
                        <CheckCircle2 size={15} className="text-green-600" />
                      ) : (
                        <XCircle size={15} className="text-red-500" />
                      )}
                    </td>
                    <td className="px-3 py-2 font-medium text-zinc-800">{c.name}</td>
                    <td className="px-3 py-2 font-mono text-[12px] text-zinc-600 truncate max-w-[240px]" title={c.target}>
                      {c.target}
                    </td>
                    <td className="px-3 py-2 text-zinc-600">{c.detail || '—'}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-zinc-400 text-[12px]">
                      {c.duration_ms}ms
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* 阶段 L：四服务端口总览（数据来自自检项 target，不额外请求） */}
      {diag && diag.checks.length > 0 && (
        <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
          <div className="flex items-center gap-2 mb-3">
            <Activity size={16} className="text-teal-600" />
            <h3 className="text-[15px] font-semibold text-zinc-900">服务端口总览</h3>
            <span className="text-[11.5px] text-zinc-400">四个独立入口 · 端口来自自检目标</span>
          </div>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
            {([
              ['manager', '管理面板'],
              ['engine', 'OpenCode 引擎'],
              ['provider', '供应商服务'],
              ['kilo', 'Kilo 独立网关'],
            ] as const).map(([name, label]) => {
              const c = diag.checks.find((x) => x.name === name)
              const port = (c?.target || '').split(':').pop() || '—'
              return (
                <div key={name} className="rounded-xl border border-zinc-200 bg-zinc-50/60 px-3 py-2.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-[12.5px] font-medium text-zinc-700">{label}</span>
                    <span className={clsx('text-[11px] rounded px-1.5 py-0.5', c?.ok ? 'bg-green-50 text-green-700' : 'bg-rose-50 text-rose-700')}>
                      {c ? (c.ok ? '正常' : '异常') : '未检查'}
                    </span>
                  </div>
                  <div className="mt-1 font-mono text-[13px] text-zinc-800">{port}</div>
                  {c?.detail && <div className="mt-0.5 truncate text-[11px] text-zinc-400" title={c.detail}>{c.detail}</div>}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* 阶段 O：OpenCode 引擎通道分析（自包含组件，不改本页 state） */}
      <ChannelStatsCard />

      {/* 引擎实时 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <Zap size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">引擎实时</h3>
          <span className="flex-1" />
          <span
            className={clsx(
              'px-2 py-0.5 rounded-full text-[11px] font-medium',
              engine?.running ? 'bg-green-50 text-green-700' : 'bg-zinc-100 text-zinc-500',
            )}
          >
            {engine?.state ?? '未知'}
          </span>
        </div>
        <div className="grid grid-cols-4 gap-3">
          <Stat label="端口" value={engine?.port ? String(engine.port) : '—'} />
          <Stat label="免费模型" value={`${engine?.model_shown ?? 0} / ${engine?.model_total ?? 0}`} />
          <Stat label="WARP" value={engine?.warp_status ?? '—'} />
          <Stat label="出口 活跃/冷却" value={`${engine?.warp_active ?? 0} / ${engine?.warp_cooling ?? 0}`} />
        </div>

        {/* 出口池按需扩容：额度主要绑 session，出口多不等于额度多，
            所以默认只开 2~4 个；并发不够时在这里手动加。 */}
        <div className="mt-3 flex items-center gap-2 flex-wrap">
          <span className="text-[12px] text-zinc-500">扩容出口池：</span>
          {[2, 4, 6, 8].map((n) => (
            <button
              key={n}
              onClick={() => void growPool(n)}
              disabled={growing !== null}
              className="px-2.5 py-1 rounded-md text-[12px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-40"
            >
              {growing === n ? <Loader2 size={12} className="animate-spin inline" /> : null} {n} 个
            </button>
          ))}
          <span className="text-[11px] text-zinc-400">
            改完需重建引擎配置并重启（每个出口注册 10~90s）
          </span>
        </div>
      </div>

      {/* 手动探测 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <Activity size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">模型探测</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">发一次最小请求，看真实状态码</span>
        </div>

        <div className="flex items-center gap-2 mb-3">
          <input
            value={probingModel}
            onChange={(e) => setProbingModel(e.target.value)}
            placeholder="模型 ID，如 big-pickle"
            className="flex-1 px-3 py-1.5 rounded-lg border border-zinc-200 text-[13px] font-mono focus:outline-none focus:ring-2 focus:ring-teal-500/30"
          />
          <button
            onClick={() => void doProbe(probingModel.trim() || undefined)}
            disabled={probing !== null}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-zinc-900 hover:bg-zinc-800 disabled:opacity-40"
          >
            {probing ? <Loader2 size={14} className="animate-spin" /> : <Activity size={14} />}
            探测
          </button>
          <button
            onClick={() => void doProbe()}
            disabled={probing !== null}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
            title="批量探测当前可用的模型（最多 20 个）"
          >
            {probing === 'all' ? <Loader2 size={14} className="animate-spin" /> : <Zap size={14} />}
            全量探测
          </button>
        </div>

        {probeNote ? (
          <div className="mb-3 rounded-lg bg-amber-50 border border-amber-200 px-3 py-2 text-[12px] text-amber-900">
            {probeNote}
          </div>
        ) : null}

        {probeResults.length > 0 ? (
          <div className="overflow-hidden rounded-xl border border-zinc-100">
            <table className="w-full text-[12px]">
              <thead className="bg-zinc-50 text-zinc-500">
                <tr>
                  <th className="text-left font-medium px-3 py-2">模型</th>
                  <th className="text-left font-medium px-3 py-2 w-20">状态</th>
                  <th className="text-right font-medium px-3 py-2 w-20">耗时</th>
                  <th className="text-left font-medium px-3 py-2">响应片段</th>
                </tr>
              </thead>
              <tbody>
                {probeResults.map((r) => (
                  <tr key={r.model} className="border-t border-zinc-100">
                    <td className="px-3 py-1.5 font-mono text-zinc-800">{r.model}</td>
                    <td className="px-3 py-1.5">
                      <span className={clsx('px-1.5 py-0.5 rounded text-[11px] font-medium', statusCls(r.status))}>
                        {r.status || 'ERR'}
                      </span>
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums text-zinc-500">{r.latency_ms}ms</td>
                    <td className="px-3 py-1.5 font-mono text-[11px] text-zinc-500 truncate max-w-[420px]" title={r.preview ?? r.error}>
                      {r.preview || r.error || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="text-[12px] text-zinc-400">尚未探测。填模型 ID 或点「全量探测」。</div>
        )}
      </div>

      {/* 失败分布 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <AlertTriangle size={16} className="text-amber-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">失败分布</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">
            {fails ? `扫描 ${fails.scanned} 条 · ${fails.total_fails} 次失败` : '—'}
          </span>
        </div>
        {!fails || fails.buckets.length === 0 ? (
          <div className="text-[12px] text-zinc-400">调用日志里没有失败记录。</div>
        ) : (
          <div className="overflow-hidden rounded-xl border border-zinc-100">
            <table className="w-full text-[13px]">
              <thead className="bg-zinc-50 text-zinc-500">
                <tr>
                  <th className="text-left font-medium px-3 py-2">厂商</th>
                  <th className="text-left font-medium px-3 py-2">错误类别</th>
                  <th className="text-right font-medium px-3 py-2 w-24">次数</th>
                  <th className="text-left font-medium px-3 py-2 w-32">最近出现</th>
                </tr>
              </thead>
              <tbody>
                {fails.buckets.map((b) => (
                  <tr key={`${b.vendor}-${b.reason}`} className="border-t border-zinc-100">
                    <td className="px-3 py-1.5 font-mono text-zinc-800">{b.vendor}</td>
                    <td className="px-3 py-1.5">
                      <span className="px-1.5 py-0.5 rounded bg-amber-50 text-amber-700 text-[11px] font-mono">
                        {b.reason}
                      </span>
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums font-medium text-zinc-900">{b.count}</td>
                    <td className="px-3 py-1.5 text-zinc-400 text-[12px]">{fmtRel(b.last_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* 调用分析（v2.0.0 增补）：模型 × 通道 × 密钥 的成功率与耗时矩阵 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex flex-wrap items-center gap-2 mb-3">
          <BarChart3 size={16} className="text-indigo-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">调用分析</h3>
          <span className="flex-1" />
          {metricLoading && <Loader2 size={13} className="animate-spin text-zinc-400" />}
          <div className="inline-flex rounded-lg border border-zinc-200 overflow-hidden text-[12px]">
            {[1, 7, 30].map((d) => (
              <button
                key={d}
                onClick={() => setMetricDays(d)}
                className={clsx(
                  'px-2.5 py-1 transition-colors',
                  metricDays === d
                    ? 'bg-indigo-600 text-white'
                    : 'bg-white text-zinc-600 hover:bg-zinc-50',
                )}
              >
                {d === 1 ? '今天' : `${d} 天`}
              </button>
            ))}
          </div>
          <button
            onClick={() => void loadMetrics(metricDays)}
            className="px-2 py-1 rounded-md text-[12px] text-zinc-600 hover:bg-zinc-100"
            title="重新读取指标文件"
          >
            刷新
          </button>
        </div>

        {/* 总览 */}
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
          <MetricStat
            label="请求数"
            value={metricSummary ? String(metricSummary.requests) : '—'}
            sub={metricSummary && metricSummary.requests > 0 ? `${metricSummary.models} 个模型 / ${metricSummary.keys} 个密钥` : undefined}
          />
          <MetricStat
            label="成功率"
            value={metricSummary && metricSummary.requests > 0 ? `${(metricSummary.ok_rate * 100).toFixed(0)}%` : '—'}
            tone={metricSummary && metricSummary.requests > 0 ? (metricSummary.ok_rate >= 0.9 ? 'good' : metricSummary.ok_rate >= 0.6 ? 'warn' : 'bad') : undefined}
            sub={metricSummary && metricSummary.requests > 0 ? `成功 ${metricSummary.ok} / 失败 ${metricSummary.fail} / 取消 ${metricSummary.canceled}` : undefined}
          />
          <MetricStat
            label="P95 耗时"
            value={metricSummary && metricSummary.p95_ms > 0 ? fmtDur2(metricSummary.p95_ms) : '—'}
            sub={metricSummary && metricSummary.p50_ms > 0 ? `P50 ${fmtDur2(metricSummary.p50_ms)} · 均值 ${fmtDur2(metricSummary.avg_ms)}` : undefined}
          />
          <MetricStat
            label="Session 轮换"
            value={metricSummary ? String(metricSummary.rotated) : '—'}
            tone={metricSummary && metricSummary.requests > 0 && metricSummary.rotated / metricSummary.requests > 0.3 ? 'warn' : undefined}
            sub={metricSummary && metricSummary.requests > 0 ? `占 ${((metricSummary.rotated / metricSummary.requests) * 100).toFixed(0)}%（轮换多说明额度消耗快）` : undefined}
          />
        </div>

        {/* 优化建议 */}
        <div className="mb-4">
          <div className="flex items-center gap-1.5 mb-2">
            <Lightbulb size={13} className="text-amber-500" />
            <span className="text-[12px] font-medium text-zinc-700">优化建议</span>
          </div>
          {!metricSuggestions || metricSuggestions.suggestions.length === 0 ? (
            <div className="text-[12px] text-zinc-400">暂无建议。</div>
          ) : (
            <div className="space-y-1.5">
              {metricSuggestions.suggestions.map((g, i) => (
                <div
                  key={i}
                  className={clsx(
                    'rounded-lg border px-3 py-2 text-[12px]',
                    g.level === 'action'
                      ? 'bg-red-50 border-red-200 text-red-900'
                      : g.level === 'warn'
                        ? 'bg-amber-50 border-amber-200 text-amber-900'
                        : 'bg-zinc-50 border-zinc-200 text-zinc-700',
                  )}
                >
                  <div>{g.text}</div>
                  {g.detail && <div className="mt-0.5 text-[11px] opacity-70">{g.detail}</div>}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* 矩阵 */}
        <div className="mb-4">
          <div className="text-[12px] font-medium text-zinc-700 mb-2">模型 × 通道 × 密钥</div>
          {!metricMatrix || metricMatrix.cells.length === 0 ? (
            <div className="text-[12px] text-zinc-400">
              暂无调用记录。指标随每次调用产生；先用一次模型再回来看。
            </div>
          ) : (
            <div className="overflow-hidden rounded-xl border border-zinc-100">
              <table className="w-full text-[12px]">
                <thead className="bg-zinc-50 text-zinc-500">
                  <tr>
                    <th className="text-left font-medium px-3 py-2">模型</th>
                    <th className="text-left font-medium px-3 py-2 w-24">通道</th>
                    <th className="text-left font-medium px-3 py-2 w-28">密钥</th>
                    {([['requests', '请求'], ['ok_rate', '成功率'], ['avg_ms', '平均耗时']] as const).map(([k, label]) => (
                      <th
                        key={k}
                        onClick={() => setMatrixSort(k)}
                        className={clsx(
                          'font-medium px-3 py-2 cursor-pointer select-none text-right',
                          matrixSort === k ? 'text-indigo-600' : 'hover:text-zinc-700',
                        )}
                        title="点击按此列排序"
                      >
                        {label}
                        {matrixSort === k ? ' ↓' : ''}
                      </th>
                    ))}
                    <th className="text-left font-medium px-3 py-2">主要失败原因</th>
                    <th className="text-right font-medium px-3 py-2 w-20">轮换</th>
                  </tr>
                </thead>
                <tbody>
                  {[...metricMatrix.cells]
                    .sort((a, b) => {
                      if (matrixSort === 'ok_rate') return a.ok_rate - b.ok_rate
                      if (matrixSort === 'avg_ms') return b.avg_ms - a.avg_ms
                      return b.requests - a.requests
                    })
                    .slice(0, 60)
                    .map((c) => (
                      <tr key={`${c.model}|${c.channel}|${c.key_id}`} className="border-t border-zinc-100">
                        <td className="px-3 py-1.5 font-mono text-zinc-800">{c.model}</td>
                        <td className="px-3 py-1.5">
                          <span
                            className={clsx(
                              'px-1.5 py-0.5 rounded text-[11px]',
                              c.channel === 'key'
                                ? 'bg-teal-50 text-teal-700'
                                : 'bg-zinc-100 text-zinc-600',
                            )}
                          >
                            {c.channel === 'key' ? '密钥' : c.channel === 'anonymous' ? '匿名' : c.channel}
                          </span>
                        </td>
                        <td className="px-3 py-1.5 font-mono text-zinc-500 text-[11px]">{c.key_id}</td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-zinc-700">{c.requests}</td>
                        <td className="px-3 py-1.5 text-right">
                          <span
                            className={clsx(
                              'px-1.5 py-0.5 rounded tabular-nums font-medium text-[11px]',
                              c.ok_rate >= 0.9
                                ? 'bg-green-50 text-green-700'
                                : c.ok_rate >= 0.6
                                  ? 'bg-amber-50 text-amber-700'
                                  : 'bg-red-50 text-red-700',
                            )}
                          >
                            {(c.ok_rate * 100).toFixed(0)}%
                          </span>
                        </td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-zinc-600">
                          {fmtDur2(c.avg_ms)}
                        </td>
                        <td className="px-3 py-1.5">
                          {c.top_reason ? (
                            <span className="px-1.5 py-0.5 rounded bg-amber-50 text-amber-700 text-[11px] font-mono">
                              {c.top_reason}
                            </span>
                          ) : (
                            <span className="text-zinc-300">—</span>
                          )}
                        </td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-zinc-500">
                          {c.rotated > 0 ? c.rotated : '—'}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          )}
          {metricMatrix && metricMatrix.cells.length > 60 && (
            <div className="mt-1.5 text-[11px] text-zinc-400">
              仅显示前 60 行（共 {metricMatrix.cells.length} 个组合）
            </div>
          )}
        </div>

        {/* 密钥健康度 */}
        <div>
          <div className="flex items-center gap-1.5 mb-2">
            <KeyRound size={13} className="text-teal-600" />
            <span className="text-[12px] font-medium text-zinc-700">密钥健康度</span>
          </div>
          {!metricKeys || metricKeys.keys.length === 0 ? (
            <div className="text-[12px] text-zinc-400">暂无密钥调用记录。</div>
          ) : (
            <div className="overflow-hidden rounded-xl border border-zinc-100">
              <table className="w-full text-[12px]">
                <thead className="bg-zinc-50 text-zinc-500">
                  <tr>
                    <th className="text-left font-medium px-3 py-2">密钥</th>
                    <th className="text-left font-medium px-3 py-2 w-20">通道</th>
                    <th className="text-right font-medium px-3 py-2 w-20">请求</th>
                    <th className="text-right font-medium px-3 py-2 w-24">成功率</th>
                    <th className="text-right font-medium px-3 py-2 w-28">平均耗时</th>
                    <th className="text-right font-medium px-3 py-2 w-20">模型数</th>
                    <th className="text-right font-medium px-3 py-2 w-20">轮换</th>
                    <th className="text-left font-medium px-3 py-2">失败原因</th>
                  </tr>
                </thead>
                <tbody>
                  {metricKeys.keys.map((k) => (
                    <tr key={k.key_id} className="border-t border-zinc-100">
                      <td className="px-3 py-1.5 font-mono text-zinc-800">{k.key_id}</td>
                      <td className="px-3 py-1.5 text-zinc-500 text-[11px]">
                        {k.channel === 'key' ? '密钥' : k.channel === 'anonymous' ? '匿名' : k.channel || '—'}
                      </td>
                      <td className="px-3 py-1.5 text-right tabular-nums text-zinc-700">{k.requests}</td>
                      <td className="px-3 py-1.5 text-right">
                        <span
                          className={clsx(
                            'px-1.5 py-0.5 rounded tabular-nums font-medium text-[11px]',
                            k.ok_rate >= 0.9
                              ? 'bg-green-50 text-green-700'
                              : k.ok_rate >= 0.6
                                ? 'bg-amber-50 text-amber-700'
                                : 'bg-red-50 text-red-700',
                          )}
                        >
                          {(k.ok_rate * 100).toFixed(0)}%
                        </span>
                      </td>
                      <td className="px-3 py-1.5 text-right tabular-nums text-zinc-600">{fmtDur2(k.avg_ms)}</td>
                      <td className="px-3 py-1.5 text-right tabular-nums text-zinc-500">{k.models}</td>
                      <td className="px-3 py-1.5 text-right tabular-nums text-zinc-500">
                        {k.rotated > 0 ? k.rotated : '—'}
                      </td>
                      <td className="px-3 py-1.5 text-[11px] font-mono text-amber-700">
                        {k.fail_reasons
                          ? Object.entries(k.fail_reasons)
                              .sort((a, b) => b[1] - a[1])
                              .map(([r, n]) => `${r}×${n}`)
                              .join(' ')
                          : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>

        <div className="mt-3 text-[11px] text-zinc-400">
          数据来自本机 <code>runtime/call-metrics/</code>，按天滚动、保留 30 天，不上传任何服务器。
          「出口」维度不可用：引擎的 <code>egress</code> / <code>exit_id</code> 字段实测恒为空。
        </div>
      </div>
    </div>
  )
}

/** fmtDur2 毫秒 → 人读耗时（接受小数毫秒，因为 avg_ms 是浮点）。 */
function fmtDur2(ms: number): string {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/** MetricStat 调用分析的数字卡（带副标题与着色）。 */
function MetricStat({
  label,
  value,
  sub,
  tone,
}: {
  label: string
  value: string
  sub?: string
  tone?: 'good' | 'warn' | 'bad'
}) {
  return (
    <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div
        className={clsx(
          'mt-0.5 text-[19px] font-semibold tabular-nums',
          tone === 'good'
            ? 'text-green-700'
            : tone === 'warn'
              ? 'text-amber-700'
              : tone === 'bad'
                ? 'text-red-700'
                : 'text-zinc-900',
        )}
      >
        {value}
      </div>
      {sub && <div className="mt-0.5 text-[10px] text-zinc-400 leading-tight">{sub}</div>}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div className="mt-1 text-[15px] font-semibold text-zinc-900 font-mono truncate" title={value}>
        {value}
      </div>
    </div>
  )
}
