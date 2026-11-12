import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Copy,
  Loader2,
  Power,
  RefreshCw,
  ShieldAlert,
  Square,
} from 'lucide-react'
import { api, type KiloGatewayStatus, type KiloModelEntry } from '../lib/api'
import VirtualModelsCard from './VirtualModelsCard'

/**
 * Kilo 独立网关页面（阶段 K）。
 *
 * 设计要点：
 *  1. 训练警示横幅常驻、不可关闭（需求本体），按用户意见精简为一句话；
 *  2. 只保留「只显示免费模型」一个开关：勾选=只免费，不勾=全部；
 *  3. 目录来自上游实时拉取且会漂移，提供手动强制刷新；
 *  4. 每次请求的上游**真实返回**模型由网关旁路观测并持久化，见【日志】页
 *     （可按供应商筛选）；本页不重复展示。
 *  5. 「试跑」按钮已移除：浏览器从 40000 页跨端口直连 30080 会被同源策略拦下
 *     （30080 不带 CORS 头），实际不可用；验证请用 curl 或真实客户端直连 30080。
 */

/** 上下文窗口展示：1048576 → 1M，128000 → 128K。 */
function fmtCtx(n?: number): string {
  if (!n) return '—'
  if (n >= 1048576) return `${Math.round(n / 1048576)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}

/** 状态徽章：ready / failed / stopped */
function stateBadge(state: string, running: boolean): [string, string] {
  if (state === 'ready' && running) return ['bg-green-50 text-green-700', '运行中']
  if (state === 'failed') return ['bg-red-50 text-red-600', '异常']
  return ['bg-zinc-100 text-zinc-500', '已停止']
}

/** 数据政策警示横幅：常驻、不可关闭（2-1：按用户意见只保留一句）。 */
function DataPolicyBanner() {
  return (
    <div className="flex items-center gap-2 rounded-2xl border border-rose-200 bg-rose-50/80 px-4 py-2.5">
      <ShieldAlert className="h-4 w-4 shrink-0 text-rose-600" />
      <span className="text-[13.5px] font-medium text-rose-800">数据可能被上游训练</span>
    </div>
  )
}

export default function KiloPage({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [status, setStatus] = useState<KiloGatewayStatus | null>(null)
  const [models, setModels] = useState<KiloModelEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<'toggle' | 'refresh' | null>(null)

  const aliveRef = useRef(true)

  const reload = useCallback(async () => {
    try {
      const s = await api.kiloStatus()
      if (aliveRef.current) setStatus(s)
    } catch {
      /* 状态读不到不阻断页面 */
    }
    try {
      const mv = await api.kiloModels()
      if (aliveRef.current) setModels(mv.models ?? [])
    } catch {
      /* 目录读不到不阻断页面 */
    }
    if (aliveRef.current) setLoading(false)
  }, [])

  useEffect(() => {
    aliveRef.current = true
    void reload()
    const t = window.setInterval(() => void reload(), 5000)
    return () => {
      aliveRef.current = false
      window.clearInterval(t)
    }
  }, [reload])

  const copyText = async (text: string, label: string) => {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      toast(`已复制${label}`)
    } catch {
      toast(`复制${label}失败`, false)
    }
  }

  const toggleEnabled = async () => {
    if (!status) return
    setBusy('toggle')
    try {
      const next = !status.enabled
      const s = await api.kiloToggle({ enabled: next })
      setStatus(s)
      toast(next ? 'Kilo 网关已启用' : 'Kilo 网关停用', next)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  /** 2-2：唯一开关，勾选=只免费，不勾=全部（直接写后端配置）。 */
  const toggleFreeOnly = async () => {
    if (!status) return
    setBusy('toggle')
    try {
      const next = !status.free_only
      const s = await api.kiloToggle({ free_only: next })
      setStatus(s)
      toast(next ? '只显示免费模型' : '显示全部模型（含付费档）', true)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  /** 2-3：手动强制实拉上游目录（绕过 10s 节流）。 */
  const refreshCatalog = async () => {
    setBusy('refresh')
    try {
      const r = await api.kiloRefresh()
      setStatus(r.status)
      toast(`目录已刷新：上游共 ${r.total} 个模型`, true)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const [badgeCls, badgeText] = stateBadge(status?.state ?? 'stopped', status?.running ?? false)
  // 2-2：后端已按 free_only 过滤，这里不再二次过滤（两个勾选合并为一个）。
  const visible = models

  return (
    <div className="space-y-4">
      {/* 训练警示：常驻、不可关闭 */}
      <DataPolicyBanner />

      {/* 状态卡 */}
      <div className="rounded-2xl border border-zinc-200 bg-white p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2.5">
            <span className={clsx('rounded-full px-2.5 py-0.5 text-[12px] font-medium', badgeCls)}>
              {badgeText}
            </span>
            <span className="text-[15px] font-semibold text-zinc-800">Kilo 独立网关</span>
            <span className="text-[12px] text-zinc-500">
              独立入口 · 不经过 OpenCode(40080) 与自定义源(50080)
            </span>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => void refreshCatalog()}
              disabled={busy !== null}
              title="强制实拉上游 /models（上游免费档会漂移）"
              className="rounded-lg border border-zinc-200 px-2.5 py-1.5 text-[12.5px] text-zinc-600 hover:bg-zinc-50 disabled:opacity-40"
            >
              {busy === 'refresh' ? (
                <Loader2 className="mr-1 inline h-3.5 w-3.5 animate-spin" />
              ) : (
                <RefreshCw className="mr-1 inline h-3.5 w-3.5" />
              )}
              刷新目录
            </button>
            <button
              onClick={() => void toggleEnabled()}
              disabled={busy !== null}
              className={clsx(
                'rounded-lg px-3 py-1.5 text-[12.5px] font-medium disabled:opacity-40',
                status?.enabled
                  ? 'border border-rose-200 bg-rose-50 text-rose-700 hover:bg-rose-100'
                  : 'border border-green-200 bg-green-50 text-green-700 hover:bg-green-100',
              )}
            >
              {busy === 'toggle' ? (
                <Loader2 className="mr-1 inline h-3.5 w-3.5 animate-spin" />
              ) : status?.enabled ? (
                <Square className="mr-1 inline h-3.5 w-3.5" />
              ) : (
                <Power className="mr-1 inline h-3.5 w-3.5" />
              )}
              {status?.enabled ? '停用' : '启用'}
            </button>
          </div>
        </div>

        {status?.last_error && (
          <div className="mt-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-[12.5px] text-red-700">
            {status.last_error}
          </div>
        )}

        {/* 端点与密钥 */}
        <div className="mt-4 grid gap-3 md:grid-cols-2">
          <div className="rounded-xl border border-zinc-200 bg-zinc-50/60 px-3 py-2.5">
            <div className="mb-1 text-[12px] text-zinc-500">Base URL（OpenAI 兼容）</div>
            <div className="flex items-center justify-between gap-2">
              <code className="break-all font-mono text-[13px] text-zinc-800">
                {status?.address ?? '—'}
              </code>
              <button
                onClick={() => void copyText(status?.address ?? '', '地址')}
                className="shrink-0 rounded border border-zinc-200 bg-white px-1.5 py-1 text-zinc-500 hover:text-zinc-800"
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>
          <div className="rounded-xl border border-zinc-200 bg-zinc-50/60 px-3 py-2.5">
            <div className="mb-1 text-[12px] text-zinc-500">API Key（统一网关密钥）</div>
            <div className="flex items-center justify-between gap-2">
              <code className="break-all font-mono text-[13px] text-zinc-800">
                {status?.api_key ?? '—'}
              </code>
              <button
                onClick={() => void copyText(status?.api_key ?? '', '密钥')}
                className="shrink-0 rounded border border-zinc-200 bg-white px-1.5 py-1 text-zinc-500 hover:text-zinc-800"
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-[12.5px] text-zinc-500">
          <span>端口：{status?.port ?? '—'}</span>
          <span>
            上游目录：{status?.model_total ?? 0} 个 · 当前展示 {status?.model_shown ?? 0} 个
          </span>
          <span>出站代理：{status?.proxy || '直连'}</span>
          {status?.last_refresh && <span>目录更新：{status.last_refresh}</span>}
        </div>
      </div>

      {/* 模型清单 */}
      <div className="rounded-2xl border border-zinc-200 bg-white p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="text-[15px] font-semibold text-zinc-800">可用模型</div>
          {/* 2-2：只保留一个开关 */}
          <label className="flex cursor-pointer items-center gap-1.5 text-[12.5px] text-zinc-600">
            <input
              type="checkbox"
              checked={status?.free_only ?? true}
              onChange={() => void toggleFreeOnly()}
              disabled={busy !== null}
            />
            只显示免费模型
          </label>
        </div>

        {/* 列表限高：超出内部滚动，表头吸顶（关掉 free_only 后 383 个模型也不会把页面撑爆）。 */}
        <div className="mt-3 max-h-[420px] overflow-y-auto overflow-x-hidden rounded-xl border border-zinc-200">
          <table className="w-full text-[12.5px]">
            <thead className="sticky top-0 z-10 bg-zinc-50 text-left text-zinc-500 shadow-[0_1px_0_0_#e4e4e7]">
              <tr>
                <th className="px-3 py-2 font-medium">模型 ID</th>
                <th className="px-3 py-2 font-medium">档位</th>
                <th className="px-3 py-2 font-medium">工具调用</th>
                <th className="px-3 py-2 font-medium">上下文</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100">
              {loading ? (
                <tr>
                  <td colSpan={4} className="px-3 py-6 text-center text-zinc-400">
                    <Loader2 className="mx-auto h-4 w-4 animate-spin" />
                  </td>
                </tr>
              ) : visible.length === 0 ? (
                <tr>
                  <td colSpan={4} className="px-3 py-6 text-center text-zinc-400">
                    目录为空（上游未拉通或已全被过滤）
                  </td>
                </tr>
              ) : (
                visible.map((m) => (
                  <tr key={m.id} className="hover:bg-zinc-50/60">
                    <td className="px-3 py-2 font-mono text-zinc-800">
                      {m.id}
                      {m.provider_name && (
                        <span className="ml-2 text-[11px] text-zinc-400">{m.provider_name}</span>
                      )}
                    </td>
                    <td className="px-3 py-2">
                      {m.free ? (
                        <span className="rounded bg-green-50 px-1.5 py-0.5 text-[11px] text-green-700">
                          免费
                        </span>
                      ) : (
                        <span className="rounded bg-zinc-100 px-1.5 py-0.5 text-[11px] text-zinc-500">
                          付费
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-zinc-500">
                      {m.supports_tools ? '支持' : '不支持'}
                    </td>
                    <td className="px-3 py-2 text-zinc-500">{fmtCtx(m.context_window)}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        <div className="mt-2 text-[11.5px] text-zinc-400">
          目录来自上游实时拉取（不硬编码）；上游免费档会漂移，可点「刷新目录」强制重拉。
        </div>
      </div>

      {/* 阶段 M：命名虚拟模型（自包含组件，不改本页现有 state） */}
      <VirtualModelsCard models={models} toast={toast} />

      {/* 阶段 L：调用记录已持久化并并入统一日志，见【日志】页（可按供应商筛选） */}
      <div className="rounded-2xl border border-zinc-200 bg-zinc-50/60 px-4 py-3 text-[12.5px] text-zinc-500">
        每次调用的真实响应模型已并入<b className="text-zinc-700">「日志」</b>页（持久化、重启不丢），
        在日志页用「供应商 → Kilo」筛选即可查看。
      </div>
    </div>
  )
}
