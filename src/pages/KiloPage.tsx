import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  AlertTriangle,
  Copy,
  Loader2,
  Play,
  Power,
  RefreshCw,
  ShieldAlert,
  Square,
} from 'lucide-react'
import { api, type KiloGatewayStatus, type KiloModelEntry } from '../lib/api'

/**
 * Kilo 独立网关页面（阶段 K）。
 *
 * 设计要点（每条都有理由，不是装饰）：
 *  1. **训练警示横幅常驻、不可关闭**：Kilo 免费档的上游条款写明请求内容可能被
 *     用于模型训练（实测 12/12 免费模型 `mayTrainOnYourPrompts=true`）。这是
 *     需求本体，所以不给「×」按钮 —— 用户可以无视，但不会被界面骗。
 *  2. **默认只勾免费模型**：上游目录 385 个模型里只有 12 个是免费档，其余是
 *     付费档；默认全开会诱导误用。
 *  3. **「实际模型」列**：上游会静默换模型（请求 kilo-auto/free，实测返回
 *     nemotron-3-ultra-550b），不显示实际模型等于让用户误判自己在用哪个模型。
 *  4. **独立入口**：本页对应 30080 端口，与 OpenCode(40080)/自定义源(50080)
 *     没有任何关联，地址与密钥单独展示。
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

/** 数据政策警示横幅：常驻、不可关闭。 */
function DataPolicyBanner({ models }: { models: KiloModelEntry[] }) {
  const risky = models.filter((m) => m.may_train).length
  return (
    <div className="rounded-2xl border border-rose-200 bg-rose-50/80 px-4 py-3">
      <div className="flex items-start gap-2.5">
        <ShieldAlert className="mt-0.5 h-4.5 w-4.5 shrink-0 text-rose-600" />
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-rose-800">
            提示词可能被上游用于训练
          </div>
          <div className="mt-1 text-[12.5px] leading-relaxed text-rose-700">
            Kilo 免费档的服务条款声明：你发送的请求内容可能被用于模型训练与记录。
            当前目录中 <b>{risky}</b> 个模型带此标注
            {models.length > 0 && `（共 ${models.length} 个在列）`}。
            <b>请勿提交个人隐私、密钥或机密业务数据。</b>
          </div>
          <div className="mt-1.5 text-[11.5px] text-rose-600/80">
            依据：NVIDIA API Trial 使用条款「do not submit personal or confidential data」；
            上游响应带 <code className="font-mono">X-Kilo-Data-Policy</code> 头标记本策略。
          </div>
        </div>
      </div>
    </div>
  )
}

export default function KiloPage({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [status, setStatus] = useState<KiloGatewayStatus | null>(null)
  const [models, setModels] = useState<KiloModelEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<'start' | 'stop' | 'toggle' | null>(null)
  const [showAll, setShowAll] = useState(false)
  // 最近一次真实调用返回的模型（用于「实际模型」列）
  const [lastActual, setLastActual] = useState<string>('')

  const aliveRef = useRef(true)

  const reload = useCallback(async () => {
    try {
      const s = await api.kiloStatus()
      if (!aliveRef.current) return
      setStatus(s)
    } catch {
      /* 状态读不到不阻断页面 */
    }
    try {
      const mv = await api.kiloModels()
      if (!aliveRef.current) return
      setModels(mv.models ?? [])
    } catch {
      /* 目录读不到不阻断页面 */
    } finally {
      if (aliveRef.current) setLoading(false)
    }
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
      toast(next ? 'Kilo 网关已启用' : 'Kilo 网关已停用', next)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const toggleFreeOnly = async () => {
    if (!status) return
    setBusy('toggle')
    try {
      const next = !status.free_only
      const s = await api.kiloToggle({ free_only: next })
      setStatus(s)
      toast(next ? '已切回「只显示免费模型」' : '已显示全部模型（含付费档）', true)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  // 试跑一次：直连 30080（绕过面板），把上游真实模型名记下来。
  const probe = async () => {
    if (!status) return
    setBusy('start')
    try {
      const res = await fetch(`${status.address}/chat/completions`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${status.api_key}`,
        },
        body: JSON.stringify({
          model: models[0]?.id ?? 'kilo-auto/free',
          max_tokens: 500,
          messages: [{ role: 'user', content: 'Reply with exactly: KILO-OK' }],
        }),
      })
      const text = await res.text()
      if (!res.ok) {
        toast(`试跑失败 HTTP ${res.status}：${text.slice(0, 160)}`, false)
        return
      }
      const j = JSON.parse(text)
      const content = j?.choices?.[0]?.message?.content ?? ''
      if (j?.model) setLastActual(j.model)
      toast(
        content
          ? `试跑成功：${content.slice(0, 60)}`
          : '试跑返回了空内容（推理型模型可能需要更大 max_tokens）',
        Boolean(content),
      )
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const [badgeCls, badgeText] = stateBadge(status?.state ?? 'stopped', status?.running ?? false)
  const visible = showAll ? models : models.filter((m) => m.free)

  return (
    <div className="space-y-4">
      {/* 训练警示：常驻、不可关闭 */}
      <DataPolicyBanner models={models} />

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
              onClick={() => void reload()}
              className="rounded-lg border border-zinc-200 px-2.5 py-1.5 text-[12.5px] text-zinc-600 hover:bg-zinc-50"
            >
              <RefreshCw className="mr-1 inline h-3.5 w-3.5" />
              刷新
            </button>
            <button
              onClick={() => void probe()}
              disabled={busy !== null || !status?.running}
              className="rounded-lg border border-zinc-200 px-2.5 py-1.5 text-[12.5px] text-zinc-600 hover:bg-zinc-50 disabled:opacity-40"
            >
              {busy === 'start' ? (
                <Loader2 className="mr-1 inline h-3.5 w-3.5 animate-spin" />
              ) : (
                <Play className="mr-1 inline h-3.5 w-3.5" />
              )}
              试跑
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
          <div className="flex items-center gap-4">
            <label className="flex cursor-pointer items-center gap-1.5 text-[12.5px] text-zinc-600">
              <input
                type="checkbox"
                checked={status?.free_only ?? true}
                onChange={() => void toggleFreeOnly()}
                disabled={busy !== null}
              />
              只显示免费模型
            </label>
            <label className="flex cursor-pointer items-center gap-1.5 text-[12.5px] text-zinc-600">
              <input
                type="checkbox"
                checked={showAll}
                onChange={() => setShowAll((v) => !v)}
              />
              显示全部目录
            </label>
          </div>
        </div>

        {lastActual && (
          <div className="mt-2.5 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-[12.5px] text-amber-800">
            最近一次试跑，上游<strong>实际返回</strong>的模型是{' '}
            <code className="font-mono">{lastActual}</code>
            {lastActual !== models[0]?.id && '（上游会静默换模型，故单独展示）'}
          </div>
        )}

        <div className="mt-3 overflow-hidden rounded-xl border border-zinc-200">
          <table className="w-full text-[12.5px]">
            <thead className="bg-zinc-50 text-left text-zinc-500">
              <tr>
                <th className="px-3 py-2 font-medium">模型 ID</th>
                <th className="px-3 py-2 font-medium">档位</th>
                <th className="px-3 py-2 font-medium">工具调用</th>
                <th className="px-3 py-2 font-medium">上下文</th>
                <th className="px-3 py-2 font-medium">数据去向</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100">
              {loading ? (
                <tr>
                  <td colSpan={5} className="px-3 py-6 text-center text-zinc-400">
                    <Loader2 className="mx-auto h-4 w-4 animate-spin" />
                  </td>
                </tr>
              ) : visible.length === 0 ? (
                <tr>
                  <td colSpan={5} className="px-3 py-6 text-center text-zinc-400">
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
                    <td className="px-3 py-2">
                      {m.may_train ? (
                        <span
                          className="inline-flex items-center gap-1 rounded bg-rose-50 px-1.5 py-0.5 text-[11px] text-rose-700"
                          title="上游声明：请求内容可能被用于模型训练"
                        >
                          <AlertTriangle className="h-3 w-3" />
                          可能用于训练
                        </span>
                      ) : (
                        <span className="text-[11px] text-zinc-400">未标注</span>
                      )}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        <div className="mt-2 text-[11.5px] text-zinc-400">
          目录来自上游实时拉取（不硬编码）；上游免费档会漂移 —— 2026-11-11 实测为 12 个。
        </div>
      </div>
    </div>
  )
}
