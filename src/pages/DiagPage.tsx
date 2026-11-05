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
} from 'lucide-react'
import {
  api,
  type DiagReport,
  type FailAggregate,
  type FreeTierEngineStatus,
  type ModelProbeResult,
} from '../lib/api'

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
