import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Cloud,
  Copy,
  ListChecks,
  Loader2,
  Plug,
  Power,
  RefreshCw,
  ScrollText,
  ShieldAlert,
  Square,
} from 'lucide-react'
import { api, type KiloGatewayStatus, type KiloModelEntry } from '../lib/api'
import VirtualModelsCard from './VirtualModelsCard'

/**
 * Kilo 独立网关页面（阶段 K / P 对齐优化）。
 *
 * 设计要点：
 *  1. 训练警示横幅常驻、不可关闭（需求本体），按用户意见精简为一句；
 *     阶段 P 起降权为细条并移到页头下，避免占据标题位。
 *  2. 只保留「只显示免费模型」一个开关：勾选=只免费，不勾=全部；
 *  3. 目录来自上游实时拉取且会漂移，提供手动强制刷新；
 *  4. 每次请求的上游**真实返回**模型由网关旁路观测并持久化，见【日志】页；
 *  5. 「试跑」已移除：浏览器从 40000 页跨端口直连 30080 会被同源策略拦下。
 *
 * 阶段 P 对齐基准（设计 skill 对比 7 个页面）：页头 p-6 + Cloud icon + h1 +
 * 状态徽章 + 右侧按钮组；主卡 p-5 shadow-sm + h3 语义标题 + teal 图标；
 * 主操作按钮 text-[13px] px-3 bg-white；模型表 th 钉宽度、ID 列 truncate。
 * 状态徽章三分支配色保持不动（与 OpenCode 同令牌），不补 starting（后端仅三态）。
 */

/** 上下文窗口展示：1048576 → 1M，128000 → 128K。 */
function fmtCtx(n?: number): string {
  if (!n) return '—'
  if (n >= 1048576) return `${Math.round(n / 1048576)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}

/** 状态徽章：ready / failed / stopped（后端仅三态，勿补 starting）。 */
function stateBadge(state: string, running: boolean): [string, string] {
  if (state === 'ready' && running) return ['bg-green-50 text-green-700', '运行中']
  if (state === 'failed') return ['bg-red-50 text-red-600', '异常']
  return ['bg-zinc-100 text-zinc-500', '已停止']
}

/** 数据政策警示：常驻、不可关闭（2-1：按用户意见只保留一句）。 */
function DataPolicyBanner() {
  return (
    <div className="flex items-center gap-2 rounded-xl border border-rose-200 bg-rose-50 px-4 py-2">
      <ShieldAlert className="h-3.5 w-3.5 shrink-0 text-rose-600" />
      <span className="text-[12.5px] font-medium text-rose-800">数据可能被上游训练</span>
    </div>
  )
}

/** 页头按钮：与其他页主操作按钮同款（text-[13px] px-3 bg-white）。 */
const BTN_BASE = 'flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-[13px] disabled:opacity-40'

/** 运行概览的单个 Stat 卡（对齐 DiagPage 的 Stat）。 */
function Stat({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div className="mt-1 truncate font-mono text-[13px] font-semibold text-zinc-900" title={title ?? value}>{value}</div>
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
  // 2-2：后端已按 free_only 过滤，这里不再二次过滤。
  const visible = models

  return (
    <div className="space-y-4 p-6">
      {/* 页头：与其他 6 页同款（icon + h1 + 状态 + 右侧按钮组） */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Cloud size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">Kilo</h1>
          <span className={clsx('flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium', badgeCls)}>
            <span className={clsx('h-1.5 w-1.5 rounded-full', status?.running ? 'bg-green-500' : 'bg-zinc-400')} />
            {badgeText}
          </span>
          <span className="hidden text-[12px] text-zinc-500 sm:inline">独立网关 · 直通 api.kilo.ai，不经过 OpenCode / 自定义源</span>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button
            onClick={() => void refreshCatalog()}
            disabled={busy !== null}
            title="强制实拉上游 /models（上游免费档会漂移）"
            className={clsx(BTN_BASE, 'border-zinc-200 bg-white text-zinc-700 hover:bg-zinc-50')}
          >
            {busy === 'refresh' ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
            刷新目录
          </button>
          <button
            onClick={() => void toggleEnabled()}
            disabled={busy !== null}
            className={clsx(
              BTN_BASE,
              status?.enabled
                ? 'border-red-200 bg-red-50 text-red-600 hover:bg-red-100'
                : 'border-teal-200 bg-teal-50 text-teal-700 hover:bg-teal-100',
            )}
          >
            {busy === 'toggle' ? (
              <Loader2 size={14} className="animate-spin" />
            ) : status?.enabled ? (
              <Square size={14} />
            ) : (
              <Power size={14} />
            )}
            {status?.enabled ? '停用' : '启用'}
          </button>
        </div>
      </div>

      {/* 训练警示：常驻、不可关闭，置于页头下方（不再占标题位） */}
      <DataPolicyBanner />

      {/* 接入信息卡：端点 + 密钥（补副行，减小留白） */}
      <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
        <div className="mb-3 flex items-center gap-2">
          <Plug size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">接入信息</h3>
          <span className="flex-1" />
          <span className="text-[11.5px] text-zinc-400">客户端用下面的地址 + 密钥直接连 30080</span>
        </div>

        {status?.last_error && (
          <div className="mb-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-[12px] text-red-700">
            {status.last_error}
          </div>
        )}

        <div className="grid gap-3 md:grid-cols-2">
          <div className="rounded-xl border border-zinc-200 bg-zinc-50/60 p-4">
            <div className="mb-1.5 text-[12px] text-zinc-500">Base URL（OpenAI 兼容）</div>
            <div className="flex items-center justify-between gap-2">
              <code className="break-all font-mono text-[13px] text-zinc-900">{status?.address ?? '—'}</code>
              <button
                onClick={() => void copyText(status?.address ?? '', '地址')}
                className="shrink-0 rounded border border-zinc-200 bg-white px-1.5 py-1 text-zinc-500 hover:text-zinc-800"
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
            </div>
            <div className="mt-1 text-[11px] text-zinc-400">/v1/chat/completions · /v1/models</div>
          </div>
          <div className="rounded-xl border border-zinc-200 bg-zinc-50/60 p-4">
            <div className="mb-1.5 text-[12px] text-zinc-500">API Key（统一网关密钥）</div>
            <div className="flex items-center justify-between gap-2">
              <code className="break-all font-mono text-[13px] text-zinc-900">{status?.api_key ?? '—'}</code>
              <button
                onClick={() => void copyText(status?.api_key ?? '', '密钥')}
                className="shrink-0 rounded border border-zinc-200 bg-white px-1.5 py-1 text-zinc-500 hover:text-zinc-800"
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
            </div>
            <div className="mt-1 text-[11px] text-zinc-400">与其它入口同款 Bearer 鉴权</div>
          </div>
        </div>

        {/* 运行概览：5 个元数据改 Stat 卡网格（对齐 DiagPage） */}
        <div className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Stat label="监听端口" value={String(status?.port ?? '—')} />
          <Stat label="上游目录 / 展示" value={`${status?.model_total ?? 0} / ${status?.model_shown ?? 0}`} />
          <Stat label="出站代理" value={status?.proxy || '直连'} />
          <Stat label="目录更新" value={status?.last_refresh ? status.last_refresh.slice(11, 19) : '—'} title={status?.last_refresh} />
        </div>
      </section>

      {/* 阶段 L：调用记录引导上移到首屏可见（模型卡之前） */}
      <div className="flex items-center gap-2 rounded-xl border border-zinc-100 bg-zinc-50/60 px-4 py-2.5 text-[12.5px] text-zinc-700">
        <ScrollText size={14} className="shrink-0 text-teal-600" />
        <span>
          每次调用的上游真实响应模型已并入<b className="text-zinc-900">「日志」</b>页（持久化、重启不丢），
          在日志页用「供应商 → Kilo」筛选即可查看。
        </span>
      </div>

      {/* 模型清单 */}
      <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <ListChecks size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">可用模型</h3>
          <span className="text-[12px] text-zinc-400">共 {status?.model_shown ?? 0} 个</span>
          <span className="flex-1" />
          {/* 2-2：只保留一个开关，checkbox 加 accent 与胶囊样式 */}
          <label className="ml-2 inline-flex cursor-pointer select-none items-center gap-1.5 rounded-md border border-teal-300 bg-teal-50 px-2.5 py-1 text-[12px] text-teal-800">
            <input
              type="checkbox"
              className="accent-teal-600"
              checked={status?.free_only ?? true}
              onChange={() => void toggleFreeOnly()}
              disabled={busy !== null}
            />
            只显示免费模型
          </label>
        </div>

        {/* 列表限高 + 表头吸顶保留；列宽钉死，长 ID 截断 */}
        <div className="max-h-[420px] overflow-y-auto overflow-x-hidden rounded-xl border border-zinc-100">
          <table className="w-full text-[12px]">
            <thead className="sticky top-0 z-10 bg-zinc-50 text-left text-zinc-500 shadow-[0_1px_0_0_var(--border)]">
              <tr>
                <th className="px-3 py-1.5 font-medium">模型 ID</th>
                <th className="w-16 px-3 py-1.5 font-medium">档位</th>
                <th className="w-20 px-3 py-1.5 font-medium">工具调用</th>
                <th className="w-20 px-3 py-1.5 font-medium">上下文</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td colSpan={4} className="px-3 py-6 text-center text-zinc-400">
                    <Loader2 className="mx-auto h-4 w-4 animate-spin" />
                  </td>
                </tr>
              ) : (
                visible.map((m) => (
                  <tr key={m.id} className="border-t border-zinc-100 hover:bg-zinc-50/60">
                    <td className="max-w-[420px] truncate px-3 py-1.5 font-mono text-zinc-900" title={m.id}>
                      {m.id}
                      {m.provider_name && <span className="ml-2 font-sans text-[11px] text-zinc-400">{m.provider_name}</span>}
                    </td>
                    <td className="whitespace-nowrap px-3 py-1.5">
                      {m.free ? (
                        <span className="rounded bg-green-50 px-1.5 py-0.5 text-[11px] text-green-700">免费</span>
                      ) : (
                        <span className="rounded bg-zinc-100 px-1.5 py-0.5 text-[11px] text-zinc-500">付费</span>
                      )}
                    </td>
                    <td className="whitespace-nowrap px-3 py-1.5 text-zinc-500">{m.supports_tools ? '支持' : '不支持'}</td>
                    <td className="whitespace-nowrap px-3 py-1.5 text-zinc-500">{fmtCtx(m.context_window)}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        {/* 空态移到表格外（对齐 OpenCodePage / StatsPage） */}
        {!loading && visible.length === 0 && (
          <div className="mt-2 rounded-lg border border-zinc-200 bg-zinc-50 px-3 py-2 text-[12px] text-zinc-600">
            目录为空（上游未拉通或已全被过滤），点「刷新目录」重试。
          </div>
        )}
        <div className="mt-1 text-[11px] text-zinc-400">
          目录来自上游实时拉取（不硬编码）；上游免费档会漂移，可点「刷新目录」强制重拉。
        </div>
      </section>

      {/* 阶段 M：命名虚拟模型（自包含组件） */}
      <VirtualModelsCard models={models} toast={toast} />
    </div>
  )
}
