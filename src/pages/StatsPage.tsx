import { Fragment, useCallback, useEffect, useState } from 'react'
import clsx from 'clsx'
import { BarChart3, ChevronDown, ChevronRight, RefreshCw, RotateCcw, Inbox, HeartPulse, Download } from 'lucide-react'
import { api, downloadText, type HealthSummary, type StatsSummary } from '../lib/api'

/** 千分位格式化 */
const fmt = (n: number) => n.toLocaleString('en-US')

function Card({
  label,
  value,
  accent,
}: {
  label: string
  value: string
  accent?: boolean
}) {
  return (
    <div className="flex-1 min-w-[150px] bg-white rounded-[16px] border border-zinc-200 shadow-sm p-4">
      <div className="text-[12px] text-zinc-500 mb-1">{label}</div>
      <div className={clsx('text-[22px] font-semibold tabular-nums', accent ? 'text-teal-700' : 'text-zinc-900')}>
        {value}
      </div>
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

  // 健康巡检状态
  const [health, setHealth] = useState<HealthSummary | null>(null)
  const [healthBusy, setHealthBusy] = useState(false)
  const [healthExpanded, setHealthExpanded] = useState(false)

  const loadHealth = useCallback(async (silent = true) => {
    try {
      const h = await api.healthSummary()
      setHealth(h)
      setHealthExpanded(false)
    } catch (e) {
      if (!silent) toast(String(e), false)
    }
  }, [toast])

  useEffect(() => {
    void loadHealth()
    const t = setInterval(() => void loadHealth(true), 15000)
    return () => clearInterval(t)
  }, [loadHealth])

  const doHealthCheck = async () => {
    setHealthBusy(true)
    try {
      const h = await api.healthCheck()
      setHealth(h)
      toast(`巡检完成：${h.healthy}/${h.total} 个实例健康`, h.unhealthy === 0)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setHealthBusy(false)
    }
  }

  const load = useCallback(
    async (silent = true) => {
      try {
        const s = await api.getStats()
        setStats(s)
        setError(null)
      } catch (e) {
        if (!silent) toast(String(e), false)
        else setError(String(e))
      }
    },
    [toast],
  )

  // 自动轮询（静默，5s）
  useEffect(() => {
    void load()
    const t = setInterval(() => void load(true), 5000)
    return () => clearInterval(t)
  }, [load])

  // 手动刷新（带 loading）
  const doRefresh = async () => {
    setRefreshing(true)
    await load(false)
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

  const doExportCsv = async () => {
    try {
      const text = await api.exportCallLogCsv()
      downloadText(`call-log-${Date.now()}.csv`, text)
      toast('日志 CSV 已导出', true)
    } catch (e) {
      toast(String(e), false)
    }
  }

  const doExportJson = async () => {
    try {
      const text = await api.exportStatsJson()
      downloadText(`stats-${Date.now()}.json`, text)
      toast('统计 JSON 已导出', true)
    } catch (e) {
      toast(String(e), false)
    }
  }

  const instances = stats?.instances ?? []
  const isEmpty = !stats || instances.length === 0
  const healthRecords = health
    ? healthExpanded
      ? health.records
      : health.records.slice(0, 4)
    : []

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
            onClick={() => void doExportCsv()}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-zinc-200 text-zinc-700 text-[12px] font-medium hover:bg-zinc-50 transition-colors"
          >
            <Download size={13} />
            导出 CSV
          </button>
          <button
            type="button"
            onClick={() => void doExportJson()}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-zinc-200 text-zinc-700 text-[12px] font-medium hover:bg-zinc-50 transition-colors"
          >
            <Download size={13} />
            导出 JSON
          </button>
          <button
            type="button"
            onClick={() => setShowResetConfirm(true)}
            disabled={resetting || !stats}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-white border border-zinc-200 text-zinc-600 text-[12px] font-medium hover:bg-zinc-50 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <RotateCcw size={13} className={resetting ? 'animate-spin' : ''} />
            {resetting ? '重置中…' : '重置统计'}
          </button>
          <button
            type="button"
            onClick={doRefresh}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-zinc-900 text-white text-[12px] font-medium hover:bg-zinc-700 transition-colors"
          >
            <RefreshCw size={13} className={refreshing ? 'animate-spin' : ''} />
            {refreshing ? '刷新中…' : '刷新'}
          </button>
        </div>
      </div>

      {/* 总览卡片 */}
      <div className="flex flex-wrap gap-4">
        <Card label="总请求数" value={fmt(stats?.total_requests ?? 0)} />
        <Card label="总输入 Token" value={fmt(stats?.total_prompt_tokens ?? 0)} />
        <Card label="总输出 Token" value={fmt(stats?.total_completion_tokens ?? 0)} />
        <Card label="总 Token" value={fmt(stats?.total_tokens ?? 0)} accent />
      </div>

      {/* 健康巡检卡片 */}
      <div className="bg-white rounded-[16px] border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-[14px] font-semibold text-zinc-900 flex items-center gap-2">
            <HeartPulse size={16} className="text-teal-700" />
            健康巡检
          </h2>
          <button
            type="button"
            onClick={() => void doHealthCheck()}
            disabled={healthBusy}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-zinc-900 text-white text-[12px] font-medium hover:bg-zinc-700 transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          >
            <RefreshCw size={13} className={healthBusy ? 'animate-spin' : ''} />
            立即巡检
          </button>
        </div>

        {health && health.total > 0 ? (
          <>
            <div className="flex flex-wrap gap-4 mb-3">
              <div className="flex items-center gap-2 text-[13px]">
                <span className="text-zinc-500">实例总数</span>
                <span className="font-semibold text-zinc-900">{health.total}</span>
              </div>
              <div className="flex items-center gap-2 text-[13px]">
                <span className="inline-block w-2 h-2 rounded-full bg-green-500" />
                <span className="text-zinc-500">健康</span>
                <span className="font-semibold text-green-700">{health.healthy}</span>
              </div>
              <div className="flex items-center gap-2 text-[13px]">
                <span className="inline-block w-2 h-2 rounded-full bg-red-500" />
                <span className="text-zinc-500">异常</span>
                <span className="font-semibold text-red-600">{health.unhealthy}</span>
              </div>
            </div>
            <table className="w-full text-[13px]">
              <thead>
                <tr className="text-left text-[12px] text-zinc-500 border-b border-zinc-100">
                  <th className="py-2 pr-3 font-medium">实例</th>
                  <th className="py-2 pr-3 font-medium">状态</th>
                  <th className="py-2 pr-3 font-medium text-right">连续失败</th>
                  <th className="py-2 font-medium">最近检查 / 错误</th>
                </tr>
              </thead>
              <tbody>
                {healthRecords.map((r) => (
                  <tr key={r.name} className="border-b border-zinc-50">
                    <td className="py-2 pr-3 font-medium text-zinc-800">{r.name}</td>
                    <td className="py-2 pr-3">
                      <span
                        className={clsx(
                          'inline-flex px-2 py-0.5 rounded-md text-[11px] font-medium',
                          r.healthy ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-600',
                        )}
                      >
                        {r.healthy ? '健康' : '异常'}
                      </span>
                    </td>
                    <td className="py-2 pr-3 text-right tabular-nums text-zinc-600">{r.consecutive_failures}</td>
                    <td className="py-2 text-zinc-500">
                      <span className="tabular-nums">
                        {r.last_check_ts ? new Date(r.last_check_ts * 1000).toLocaleTimeString() : '—'}
                      </span>
                      {r.last_error && <span className="text-red-500"> · {r.last_error}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {health.records.length > 4 && (
              <div className="mt-2 flex items-center gap-2 text-[11px]">
                <button
                  type="button"
                  onClick={() => setHealthExpanded((expanded) => !expanded)}
                  className="text-zinc-400 hover:text-zinc-600"
                >
                  {healthExpanded ? '收起' : `展开全部 (${health.records.length})`}
                </button>
                {!healthExpanded && <span className="text-zinc-400">{health.records.length - 4} 个实例已折叠</span>}
              </div>
            )}
            <div className="mt-3 text-[11px] text-zinc-400">
              每 15 秒自动刷新 · 连续失败达到「设置-健康巡检」阈值时自动重启实例
            </div>
          </>
        ) : (
          <div className="py-6 flex flex-col items-center gap-2 text-zinc-400">
            <HeartPulse size={24} strokeWidth={1.5} />
            <span className="text-[13px]">
              暂无运行中的实例。启动实例后点击「立即巡检」查看健康状态
            </span>
          </div>
        )}
      </div>

      {error && !stats && (
        <div className="text-[13px] text-red-600 bg-red-50 border border-red-100 rounded-xl px-4 py-3">
          加载失败：{error}
        </div>
      )}

      {/* 实例表格 */}
      <div className="bg-white rounded-[16px] border border-zinc-200 shadow-sm p-5">
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
                        <td colSpan={6} className="py-2 px-4">
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
