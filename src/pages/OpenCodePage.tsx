import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Copy,
  Key,
  Loader2,
  Play,
  Power,
  RefreshCw,
  Settings2,
  ShieldCheck,
  Waves,
  Zap,
} from 'lucide-react'
import { api, type FreeTierEngineStatus, type ConfigView } from '../lib/api'

/** 引擎状态徽章：ready / starting / failed / stopped。 */
function stateBadge(state: string, running: boolean): [string, string] {
  if (state === 'ready' && running) return ['bg-green-50 text-green-700', '运行中']
  if (state === 'starting') return ['bg-amber-50 text-amber-700', '启动中']
  if (state === 'failed') return ['bg-red-50 text-red-600', '异常']
  return ['bg-zinc-100 text-zinc-500', '已停止']
}

export default function OpenCodePage({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [status, setStatus] = useState<FreeTierEngineStatus | null>(null)
  const [config, setConfig] = useState<ConfigView | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<'start' | 'stop' | 'restart' | 'config' | null>(null)

  // 出口代理编辑草稿（多行文本；保存时按行提交）
  const [proxyDraft, setProxyDraft] = useState('')
  const [proxyDirty, setProxyDirty] = useState(false)
  const [savingProxy, setSavingProxy] = useState(false)

  // 付费密钥草稿（zen = 免费层 v1 通道；go = Go 订阅通道）
  const [zenDraft, setZenDraft] = useState('')
  const [goDraft, setGoDraft] = useState('')
  const [keyDirty, setKeyDirty] = useState(false)
  const [savingKeys, setSavingKeys] = useState(false)

  // WARP 参数草稿
  const [warpPool, setWarpPool] = useState('2')
  const [warpSource, setWarpSource] = useState('')
  const [warpDirty, setWarpDirty] = useState(false)
  const [savingWarp, setSavingWarp] = useState(false)

  const aliveRef = useRef(true)
  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  const reload = useCallback(async () => {
    try {
      const s = await api.freeTierEngineStatus()
      if (!aliveRef.current) return
      setStatus(s)
    } catch (e) {
      if (aliveRef.current) setStatus(null)
    }
    try {
      const c = await api.configGet()
      if (!aliveRef.current) return
      setConfig(c)
      if (!proxyDirty) setProxyDraft((c.engine_proxies ?? []).join('\n'))
      if (!keyDirty) {
        setZenDraft((c.engine_zen_keys ?? []).join('\n'))
        setGoDraft((c.engine_go_keys ?? []).join('\n'))
      }
      if (!warpDirty) {
        setWarpPool(String(c.engine_warp_pool_size || 2))
        setWarpSource(c.engine_warp_source_ipv6 ?? '')
      }
    } catch {
      /* 配置读取失败不阻断状态展示 */
    } finally {
      if (aliveRef.current) setLoading(false)
    }
  }, [proxyDirty, warpDirty, keyDirty])

  useEffect(() => {
    void reload()
    const t = window.setInterval(() => void reload(), 5000)
    return () => window.clearInterval(t)
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

  const doAction = async (kind: 'start' | 'stop' | 'restart') => {
    setBusy(kind)
    try {
      const fn =
        kind === 'start'
          ? api.freeTierEngineStart
          : kind === 'stop'
            ? api.freeTierEngineStop
            : api.freeTierEngineRestart
      const s = await fn()
      setStatus(s)
      toast(
        kind === 'start' ? '引擎已启动' : kind === 'stop' ? '引擎已停止' : '引擎已重启',
        kind !== 'stop',
      )
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const doWriteConfig = async () => {
    setBusy('config')
    try {
      const r = await api.freeTierEngineWriteConfig()
      toast(`配置已生成：${r.path}`)
      await reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const saveProxy = async () => {
    setSavingProxy(true)
    try {
      await api.configSet('engine_proxies', proxyDraft)
      setProxyDirty(false)
      toast('出口代理已保存（引擎将在下次重载时使用）')
      await reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setSavingProxy(false)
    }
  }

  const saveKeys = async () => {
    setSavingKeys(true)
    try {
      await api.configSet('engine_zen_keys', zenDraft)
      await api.configSet('engine_go_keys', goDraft)
      setKeyDirty(false)
      toast('密钥已保存，重启引擎后生效')
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setSavingKeys(false)
    }
  }

  const saveWarp = async () => {
    setSavingWarp(true)
    try {
      await api.configSet('engine_warp_pool_size', warpPool.trim() || '0')
      await api.configSet('engine_warp_source_ipv6', warpSource.trim())
      setWarpDirty(false)
      toast('WARP 参数已保存')
      await reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setSavingWarp(false)
    }
  }

  const [stateCls, stateText] = stateBadge(status?.state ?? 'stopped', status?.running ?? false)
  const warpOn = config?.engine_warp_enabled ?? true
  const engineOn = config?.engine_enabled ?? true

  return (
    <div className="p-6 space-y-4">
      {/* 工具条 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Zap size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">OpenCode</h1>
          <span
            className={clsx(
              'flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium',
              stateCls,
            )}
          >
            <span
              className={clsx(
                'w-1.5 h-1.5 rounded-full',
                status?.running ? 'bg-green-500' : 'bg-zinc-400',
              )}
            />
            {stateText}
          </span>
          {status?.warp_active ? (
            <span className="px-2 py-0.5 rounded-full bg-teal-50 text-teal-700 text-xs font-medium">
              {status.warp_active} 个出口活跃
            </span>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => void reload()}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
            刷新
          </button>
          {status?.running ? (
            <button
              onClick={() => void doAction('stop')}
              disabled={busy !== null}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-red-600 bg-red-50 hover:bg-red-100 disabled:opacity-40"
            >
              {busy === 'stop' ? <Loader2 size={14} className="animate-spin" /> : <Power size={14} />}
              停止
            </button>
          ) : (
            <button
              onClick={() => void doAction('start')}
              disabled={busy !== null || !status?.bin_exists}
              title={status?.bin_exists ? '' : '引擎未安装：请把 opencode2api-win 放入 bin/ 目录'}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
            >
              {busy === 'start' ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
              启动
            </button>
          )}
          <button
            onClick={() => void doAction('restart')}
            disabled={busy !== null || !status?.bin_exists}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
          >
            {busy === 'restart' ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
            重启
          </button>
          <button
            onClick={() => void doWriteConfig()}
            disabled={busy !== null}
            title="按当前面板设置重新生成引擎 config.json"
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-40"
          >
            {busy === 'config' ? <Loader2 size={14} className="animate-spin" /> : <Settings2 size={14} />}
            重新生成配置
          </button>
        </div>
      </div>

      {/* 引擎接入卡（沿用「统一网关」卡的信息层级） */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-4">
          <ShieldCheck size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">接入信息</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">
            {status?.error
              ? status.error
              : status?.version
                ? `引擎 ${status.version}`
                : status?.bin_exists
                  ? '引擎已安装'
                  : '引擎未安装'}
          </span>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="text-[12px] text-zinc-500 mb-1.5">OpenCode API 地址</div>
            <button
              onClick={() => void copyText(status?.address ?? '', 'API 地址')}
              className="flex items-center gap-1 text-teal-700 hover:underline"
              title="点击复制"
            >
              <code className="text-[13px]">
                {status?.address ?? `http://127.0.0.1:${config?.gateway_port_effective ?? 40080}/v1`}
              </code>
              <Copy size={12} />
            </button>
            <div className="mt-1 text-[11px] text-zinc-400">
              {status?.model_shown ?? 0} / {status?.model_total ?? 0} 个免费模型可用
            </div>
          </div>

          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="text-[12px] text-zinc-500 mb-1.5">API 密钥</div>
            <button
              onClick={() => void copyText(status?.api_key ?? '', 'API 密钥')}
              className="flex items-center gap-1 text-zinc-600 hover:underline"
              title="点击复制"
            >
              <code className="text-[13px]">{status?.api_key ?? '—'}</code>
              <Copy size={12} />
            </button>
            <div className="mt-1 text-[11px] text-zinc-400">配置客户端时使用此地址 + 密钥</div>
          </div>
        </div>

        {!status?.bin_exists && (
          <div className="mt-3 rounded-lg bg-amber-50 border border-amber-200 px-3 py-2 text-[12px] text-amber-900">
            未找到引擎二进制：
            <code className="mx-1 text-[11px]">{status?.bin_path || 'bin/opencode2api-win.exe'}</code>
            —— 请把 opencode2api-win 放入该目录后点「启动」。
          </div>
        )}
      </div>

      {/* WARP 出口池 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-4">
          <Waves size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">WARP 出口池</h3>
          <span className="flex-1" />
          <span
            className={clsx(
              'px-2 py-0.5 rounded-full text-[11px] font-medium',
              status?.warp_status === 'ready'
                ? 'bg-green-50 text-green-700'
                : 'bg-zinc-100 text-zinc-500',
            )}
          >
            {status?.warp_status || (warpOn ? '未就绪' : '已关闭')}
            {status?.warp_stack ? ` · ${status.warp_stack}` : ''}
          </span>
        </div>

        <div className="grid grid-cols-3 gap-4 mb-4">
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="text-[12px] text-zinc-500">活跃出口</div>
            <div className="mt-1 text-[20px] font-semibold text-zinc-900">{status?.warp_active ?? 0}</div>
          </div>
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="text-[12px] text-zinc-500">冷却中</div>
            <div className="mt-1 text-[20px] font-semibold text-zinc-900">{status?.warp_cooling ?? 0}</div>
          </div>
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="text-[12px] text-zinc-500">池大小上限</div>
            <div className="mt-1 text-[20px] font-semibold text-zinc-900">
              {config?.engine_warp_pool_size || 2}
            </div>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <label className="block">
            <div className="text-[12px] text-zinc-500 mb-1">出口数上限（1~32）</div>
            <input
              value={warpPool}
              onChange={(e) => {
                setWarpPool(e.target.value)
                setWarpDirty(true)
              }}
              inputMode="numeric"
              className="w-full px-3 py-1.5 rounded-lg border border-zinc-200 text-[13px] focus:outline-none focus:ring-2 focus:ring-teal-500/30"
            />
          </label>
          <label className="block">
            <div className="text-[12px] text-zinc-500 mb-1">源 IPv6 前缀（/64，留空 = 不轮换）</div>
            <input
              value={warpSource}
              onChange={(e) => {
                setWarpSource(e.target.value)
                setWarpDirty(true)
              }}
              placeholder="2001:db8:1:2::/64"
              className="w-full px-3 py-1.5 rounded-lg border border-zinc-200 text-[13px] focus:outline-none focus:ring-2 focus:ring-teal-500/30"
            />
          </label>
        </div>

        {warpDirty && (
          <div className="mt-3 flex items-center gap-2">
            <button
              onClick={() => void saveWarp()}
              disabled={savingWarp}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-zinc-900 hover:bg-zinc-800 disabled:opacity-50"
            >
              {savingWarp ? <Loader2 size={14} className="animate-spin" /> : null}
              保存
            </button>
            <button
              onClick={() => {
                setWarpDirty(false)
                setWarpPool(String(config?.engine_warp_pool_size || 2))
                setWarpSource(config?.engine_warp_source_ipv6 ?? '')
              }}
              className="px-3 py-1.5 rounded-lg text-[13px] text-zinc-600 hover:bg-zinc-100"
            >
              取消
            </button>
          </div>
        )}

        <div className="mt-3 text-[11px] text-zinc-400">
          WARP 出口每个获得一个独立的公网 IPv6；出口被上游限流后会冷却并自动替换。
          关闭 WARP 时免费通道回落到底下的代理列表。
        </div>
      </div>

      {/* 付费密钥 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <Key size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">密钥通道</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">
            {`zen ${config?.engine_zen_key_count ?? 0} 个 · go ${config?.engine_go_key_count ?? 0} 个`}
          </span>
        </div>
        <div className="mb-2 rounded-lg bg-amber-50 border border-amber-200 px-3 py-2 text-[12px] text-amber-900">
          实测结论：<b>完全不配密钥（纯匿名）时，big-pickle 等模型会稳定返回 429</b>
          「Free usage limit reached」。配上一个 zen key 后，同一模型立刻可用。
          免费层建议至少填一个 <code>oc_sk_...</code> 密钥。
        </div>
        <div className="grid grid-cols-2 gap-4">
          <label className="block">
            <div className="text-[12px] text-zinc-500 mb-1">OpenCode 密钥（oc_sk_...，每行一个）</div>
            <textarea
              value={zenDraft}
              onChange={(e) => { setZenDraft(e.target.value); setKeyDirty(true) }}
              rows={3}
              placeholder="oc_sk_xxxxxxxx"
              className="w-full px-3 py-2 rounded-lg border border-zinc-200 text-[12px] font-mono focus:outline-none focus:ring-2 focus:ring-teal-500/30"
            />
          </label>
          <label className="block">
            <div className="text-xs text-gray-500 mb-1">订阅密钥（可选）</div>
            <textarea
              value={goDraft}
              onChange={(e) => { setGoDraft(e.target.value); setKeyDirty(true) }}
              rows={3}
              placeholder="（无则留空）"
              className="w-full px-3 py-2 rounded-lg border border-zinc-200 text-[12px] font-mono focus:outline-none focus:ring-2 focus:ring-teal-500/30"
            />
          </label>
        </div>
        <div className="mt-2 flex items-center gap-2">
          <button
            onClick={() => void saveKeys()}
            disabled={!keyDirty || savingKeys}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-zinc-900 hover:bg-zinc-800 disabled:opacity-40"
          >
            {savingKeys ? <Loader2 size={14} className="animate-spin" /> : null}
            保存密钥
          </button>
          <span className="text-[11px] text-zinc-400">保存后需「重启」引擎生效</span>
        </div>
      </div>

      {/* 出口代理 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <Settings2 size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">出口代理</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">每行一个；留空 = 直连</span>
        </div>
        <textarea
          value={proxyDraft}
          onChange={(e) => {
            setProxyDraft(e.target.value)
            setProxyDirty(true)
          }}
          rows={4}
          placeholder={'direct\nsocks5://127.0.0.1:7897\nhttp://127.0.0.1:7890'}
          className="w-full px-3 py-2 rounded-lg border border-zinc-200 text-[13px] font-mono focus:outline-none focus:ring-2 focus:ring-teal-500/30"
        />
        <div className="mt-2 flex items-center gap-2">
          <button
            onClick={() => void saveProxy()}
            disabled={!proxyDirty || savingProxy}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-zinc-900 hover:bg-zinc-800 disabled:opacity-40"
          >
            {savingProxy ? <Loader2 size={14} className="animate-spin" /> : null}
            保存
          </button>
          <button
            onClick={() => {
              setProxyDraft((config?.engine_proxies ?? []).join('\n'))
              setProxyDirty(false)
            }}
            disabled={!proxyDirty}
            className="px-3 py-1.5 rounded-lg text-[13px] text-zinc-600 hover:bg-zinc-100 disabled:opacity-40"
          >
            撤销
          </button>
          <span className="text-[11px] text-zinc-400">
            支持 http / https / socks5 / socks5h；direct 表示直连
          </span>
        </div>
      </div>

      {/* 免费模型 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-3">
          <Zap size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">免费模型</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">
            {status?.model_shown ?? 0} 个可用 / 共 {status?.model_total ?? 0} 个
          </span>
        </div>
        <div className="text-[12px] text-zinc-500">
          引擎会自动探测每个免费模型的可用性，可用的才对外暴露。模型清单与可用性由引擎维护，
          这里只做只读展示——刷新详情请用右上角「刷新」。
        </div>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
            <div className="text-[11px] text-zinc-500">模型总数</div>
            <div className="text-[16px] font-semibold text-zinc-900">{status?.model_total ?? 0}</div>
          </div>
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
            <div className="text-[11px] text-zinc-500">对外可用</div>
            <div className="text-[16px] font-semibold text-green-700">{status?.model_shown ?? 0}</div>
          </div>
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-3">
            <div className="text-[11px] text-zinc-500">引擎总开关</div>
            <div className="text-[16px] font-semibold text-zinc-900">{engineOn ? '启用' : '停用'}</div>
          </div>
        </div>
      </div>
    </div>
  )
}
