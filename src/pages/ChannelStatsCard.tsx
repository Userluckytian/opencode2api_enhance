import { useCallback, useEffect, useState } from 'react'
import clsx from 'clsx'
import { Activity, RefreshCw } from 'lucide-react'
import { api, type ChannelStats } from '../lib/api'

/**
 * 阶段 O：OpenCode 引擎通道分析。
 *
 * 自包含组件（不碰 DiagPage 现有 state）：只读 /api/admin/channel-stats，
 * 展示 WARP 匿名 / key 两类通道的请求数、成功失败、占比与结论，
 * 让用户能直接看出「具体走哪个通道多」。
 */

type Bucket = { requests: number; ok: number; fail: number }

function pct(n: number, total: number): string {
  if (total <= 0) return '0%'
  return `${Math.round((n / total) * 100)}%`
}

function Bar({ label, bucket, total, tone }: { label: string; bucket: Bucket; total: number; tone: 'warp' | 'key' | 'other' }) {
  const rate = bucket.requests > 0 ? Math.round((bucket.ok / bucket.requests) * 100) : 0
  const barCls = tone === 'warp' ? 'bg-violet-500' : tone === 'key' ? 'bg-emerald-500' : 'bg-zinc-400'
  return (
    <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
      <div className="flex items-center justify-between gap-2">
        <span className={clsx('text-[12.5px] font-medium', tone === 'warp' ? 'text-violet-700' : tone === 'key' ? 'text-emerald-700' : 'text-zinc-600')}>
          {label}
        </span>
        <span className="text-[11px] text-zinc-500 tabular-nums">
          {bucket.requests} 次 · 占 {pct(bucket.requests, total)}
        </span>
      </div>
      {/* 占比条 */}
      <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-zinc-200">
        <div className={clsx('h-full rounded-full', barCls)} style={{ width: pct(bucket.requests, total) }} />
      </div>
      <div className="mt-1.5 flex items-center gap-3 text-[11.5px] text-zinc-500">
        <span>成功 <b className="text-green-700">{bucket.ok}</b></span>
        <span>失败 <b className="text-rose-700">{bucket.fail}</b></span>
        <span>成功率 <b className="text-zinc-700">{rate}%</b></span>
      </div>
    </div>
  )
}

export default function ChannelStatsCard() {
  const [stats, setStats] = useState<ChannelStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const s = await api.channelStats()
      setStats(s)
      setError('')
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const t = window.setInterval(() => void load(), 15000)
    return () => window.clearInterval(t)
  }, [load])

  const total = stats?.total ?? 0
  const warp = stats?.warp ?? { requests: 0, ok: 0, fail: 0 }
  const key = stats?.key ?? { requests: 0, ok: 0, fail: 0 }
  const other = stats?.other ?? { requests: 0, ok: 0, fail: 0 }

  let conclusion = '暂无调用记录'
  if (total > 0) {
    if (warp.requests === 0) {
      conclusion = '当前未走 WARP 匿名通道，全部引擎请求都落在 key 通道'
    } else if (key.requests === 0) {
      conclusion = '当前全部走 WARP 匿名通道，未消耗 key 额度'
    } else {
      conclusion = `key 通道占 ${pct(key.requests, total)}、WARP 匿名通道占 ${pct(warp.requests, total)}`
    }
  }

  return (
    <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
      <div className="flex items-center gap-2 mb-3">
        <Activity size={16} className="text-teal-600" />
        <h3 className="text-[15px] font-semibold text-zinc-900">通道分析</h3>
        <span className="flex-1" />
        <span className="text-[11.5px] text-zinc-400">
          {stats ? `共 ${total} 条 · ${stats.generated_at.slice(11, 19)}` : 'OpenCode 引擎 WARP 匿名 / key'}
        </span>
        <button
          onClick={() => void load()}
          className="rounded-lg border border-zinc-200 px-2 py-1 text-[11.5px] text-zinc-600 hover:bg-zinc-50"
        >
          <RefreshCw size={12} className="mr-1 inline" />
          刷新
        </button>
      </div>

      {loading ? (
        <div className="py-6 text-center text-[12px] text-zinc-400">加载中…</div>
      ) : error ? (
        <div className="rounded-xl border border-rose-200 bg-rose-50 px-3 py-4 text-center text-[12.5px] text-rose-700">
          加载失败：{error}
        </div>
      ) : (
        <>
          <div className="grid gap-2 sm:grid-cols-3">
            <Bar label="WARP 匿名通道" bucket={warp} total={total} tone="warp" />
            <Bar label="key 通道" bucket={key} total={total} tone="key" />
            <Bar label="其它 / 非引擎" bucket={other} total={total} tone="other" />
          </div>
          <div className="mt-3 rounded-xl border border-zinc-100 bg-zinc-50/60 px-3 py-2.5 text-[12.5px] text-zinc-700">
            {conclusion}
            {stats && stats.inferred > 0 && (
              <span className="ml-1 text-[11px] text-zinc-400">
                （含 {stats.inferred} 条旧日志按厂商推断，未标注 channel 原值）
              </span>
            )}
          </div>
          <div className="mt-1.5 text-[11px] text-zinc-400">
            数据来自调用日志现算（只读）；匿名通道 = 引擎 stdout 的 channel=anonymous，key 通道 = channel=key（zen/go）。
          </div>
        </>
      )}
    </div>
  )
}
