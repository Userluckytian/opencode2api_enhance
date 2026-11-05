import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Activity,
  AlertCircle,
  Copy,
  Key,
  ListChecks,
  Loader2,
  Play,
  Power,
  RefreshCw,
  Settings2,
  ShieldCheck,
  Waves,
  Zap,
} from 'lucide-react'
import { api, type FreeTierEngineStatus, type FreeTierModelsView, type ConfigView } from '../lib/api'

/** 上下文档位：1048576 → 1M，200000 → 200K，0 → —。 */
function fmtCtx(n?: number): string {
  if (!n) return '—'
  if (n >= 1048576) return `${Math.round(n / 1048576)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}

/** 相对时间：把 RFC3339 时间戳转成「3 分钟后 / 已过期 / —」。 */
function fmtRelTime(ts?: string): string {
  if (!ts) return '—'
  const t = Date.parse(ts)
  if (!Number.isFinite(t)) return '—'
  const diff = t - Date.now()
  if (diff <= 0) return '已到期'
  const min = Math.round(diff / 60000)
  if (min < 60) return `${min} 分钟后`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr} 小时后`
  return `${Math.round(hr / 24)} 天后`
}

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

  // 可用模型列表（合并引擎 /v1/models 与 availability 探测结果）
  const [models, setModels] = useState<FreeTierModelsView | null>(null)
  const [showAllModels, setShowAllModels] = useState(false)
  // 「只使用免费模型」：写 engine_free_only → 引擎 models.free_only
  const [freeOnly, setFreeOnly] = useState(true)
  const [freeOnlyDirty, setFreeOnlyDirty] = useState(false)
  const [savingFreeOnly, setSavingFreeOnly] = useState(false)
  // restartingAt 非 0 = 引擎正在重启，用它驱动状态轮询与「重启中」提示
  const [restartingAt, setRestartingAt] = useState(0)
  const restartingAtRef = useRef(0)
  const probingModelsRef = useRef<Set<string>>(new Set())

  // reloadRef 让轮询 effect 能触发一次完整刷新，又不把 reload 放进依赖数组
  const reloadRef = useRef<() => Promise<void>>(async () => {})
  useEffect(() => {
    restartingAtRef.current = restartingAt
  }, [restartingAt])
  // 逐模型探测状态：按模型 ID 分槽，互不影响。
  //
  // 早先是单个 `probingModel: string | null` + 单个 `probeResult`，于是
  // 点 A 的探测按钮会把**所有行**一起 disabled，且 A 的结果会被 B 覆盖。
  // 探测本身是每模型独立的上游请求，各行的忙态与结果本就该独立。
  const [probingModels, setProbingModels] = useState<Set<string>>(() => new Set())
  const [probeResults, setProbeResults] = useState<
    Record<string, { ok: boolean; status: number }>
  >({})

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
    } catch {
      if (aliveRef.current) setStatus(null)
    }
    try {
      const mv = await api.freeTierModels()
      if (!aliveRef.current) return
      // 重启窗口内引擎未监听是预期的：此时 mv.error 只是 transport 失败，
      // 不要用它覆盖已有列表（否则重启一次列表就闪成红字，用户以为坏了）。
      if (!(mv.error && restartingAtRef.current > 0)) setModels(mv)
    } catch {
      /* 引擎未启动时忽略 */
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
      // 免费开关以服务端为准（勾选后会有一次重启，期间别被本地态覆盖）
      if (!freeOnlyDirty) setFreeOnly(c.engine_free_only !== false)
    } catch {
      /* 配置读取失败不阻断状态展示 */
    } finally {
      if (aliveRef.current) setLoading(false)
    }
  }, [proxyDirty, warpDirty, keyDirty, freeOnlyDirty])

  // 把最新的 reload 挂到 ref（轮询 effect 用它做收尾刷新）
  useEffect(() => {
    reloadRef.current = reload
  }, [reload])

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

  // doAction 启停/重启引擎。
  //
  // 重启与「切换只使用免费模型」共用同一套体验：都走 async（后端后台重启）
  // + restartingAt 轮询。这样顶部「重启」按钮和卡片里的「引擎重启中」是同
  // 一个状态源，不会再出现「下面说重启中、上面按钮还能再点一次」。
  const doAction = async (kind: 'start' | 'stop' | 'restart') => {
    setBusy(kind)
    try {
      if (kind === 'restart') {
        const s = await api.freeTierEngineRestartAsync()
        setStatus(s)
        setRestartingAt(Date.now())
        toast('引擎正在重启（WARP 首次出口注册最长 90s）', true)
        return
      }
      const fn = kind === 'start' ? api.freeTierEngineStart : api.freeTierEngineStop
      const s = await fn()
      setStatus(s)
      toast(kind === 'start' ? '引擎已启动' : '引擎已停止', kind !== 'stop')
      void reload()
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

  // probeOne 对单个模型发一次最小请求，用真实状态码判定可用性。
  //
  // 为什么放在每行：引擎的 availability 探测是周期性的（每模型 ~1 小时一轮），
  // 用户想「现在就确认这个模型能不能用」时需要手动触发。
  const probeOne = async (modelId: string) => {
    // 同一行重复点：直接忽略（已在探测中）
    if (probingModelsRef.current.has(modelId)) return
    setProbingModels((prev) => new Set(prev).add(modelId))
    try {
      const r = await api.freeTierProbe(modelId)
      const first = r.results[0]
      if (first) {
        // 只写自己那一槽，不动其它模型的结果
        setProbeResults((prev) => ({ ...prev, [modelId]: { ok: first.ok, status: first.status } }))
      }
      if (r.note) toast(r.note, false)
      else toast(`${modelId} → ${first?.status ?? 'ERR'}（${first?.latency_ms ?? 0}ms）`, first?.ok)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      if (aliveRef.current) {
        setProbingModels((prev) => {
          const n = new Set(prev)
          n.delete(modelId)
          return n
        })
      }
    }
  }

  // saveKeys 保存密钥并让它立刻生效。
  //
  // 三步必须串起来，否则用户会以为「填了 key 却没变化」：
  //   1) 写入 config.engine_zen_keys / engine_go_keys
  //   2) 按新 key 数量重算 WARP 出口池并重新生成引擎 config.json
  //      （引擎把 key 均匀绑定到出口，出口数不足会让多个 key 挤同一出口 IP）
  //   3) 重启引擎让新配置生效
  const saveKeys = async () => {
    setSavingKeys(true)
    try {
      await api.configSet('engine_zen_keys', zenDraft)
      await api.configSet('engine_go_keys', goDraft)
      setKeyDirty(false)

      // 出口池交给后端按 key 数量推导（无 key=2，有 key=4）。
      // 这里不再前端硬算：额度主要绑 session 而非出口数，开大出口只会拖慢启动。
      setWarpDirty(false)

      // 重写引擎配置 + 重启，让 key 立即生效
      await api.freeTierEngineWriteConfig()
      const st = await api.freeTierEngineRestart()
      setStatus(st)
      toast(`密钥已生效，引擎正在按 ${zenDraft.split(String.fromCharCode(10)).filter((s) => s.trim()).length + goDraft.split(String.fromCharCode(10)).filter((s) => s.trim()).length} 个 key 重建出口池`)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setSavingKeys(false)
    }
  }

  // toggleFreeOnly 切换「只使用免费模型」。
  //
  // 引擎的 models.free_only 只在**启动时**读一次（gateway.go:128
  // catalog.SetFreeOnly(cfg.Models.FreeOnly)），没有热更新接口，所以必须
  // 重写 config.json 再重启引擎才能生效。
  //
  // 为什么重启走 async：开了 WARP 后 StartFreeTierEngine 内部的 waitForPort
  // 最长等 95s（首次出口注册）。同步等会把「点一个勾选框」变成转圈一分半，
  // 用户根本无法判断是卡死还是正常。改成写完配置立刻返回、引擎后台重启，
  // 面板用轮询把真实进度显示出来。
  const toggleFreeOnly = async (next: boolean) => {
    setFreeOnly(next)
    setFreeOnlyDirty(true)
    setSavingFreeOnly(true)
    try {
      await api.configSet('engine_free_only', next ? 'true' : 'false')
      await api.freeTierEngineWriteConfig()
      setFreeOnlyDirty(false)
      // 判断依据是「引擎是否处于停止态」，**不是** status.running。
      //
      // status.running 来自 /healthz 探活：引擎正在启动（starting，WARP 首次注册
      // 最长 90s）时它是 false。早先用 running 做条件，于是「引擎正在启动」
      // 期间切换开关会走 else 分支——只写配置、不重启，用户看到勾选框变了但
      // 引擎永远不应用新值（要等下一次别的原因重启才生效），静默失败。
      // 只有「明确停止 / 从未启动」才跳过重启；starting 与 failed 都要重建进程。
      const engineStopped = !status || status.state === 'stopped'
      if (!engineStopped) {
        const st = await api.freeTierEngineRestartAsync()
        setStatus(st)
        setRestartingAt(Date.now())
        toast(next ? '已切换到「只使用免费模型」，引擎正在重启…' : '已放开全部模型，引擎正在重启…')
      } else {
        toast(next ? '已设置「只使用免费模型」（引擎下次启动生效）' : '已放开全部模型（引擎下次启动生效）')
        void reload()
      }
    } catch (e) {
      // 失败回滚本地态，避免勾选框与后端不一致
      setFreeOnly(!next)
      setFreeOnlyDirty(false)
      toast((e as Error).message, false)
    } finally {
      setSavingFreeOnly(false)
    }
  }

  // 重启窗口内轮询状态：引擎一起来就刷新模型列表并清掉「重启中」提示。
  // 上限 150s（95s 端口等待 + 启动余量），超时放弃，避免无限轮询。
  // 两个阶段，不能只看「running && healthy」：
  //
  //   phase 1（等它停）：async restart 返回时引擎往往还活着（stop 还没执行到），
  //           这时立刻查状态会得到 running=true，误判「重启已完成」——
  //           于是「重启中」提示闪现即消失。必须先看到引擎离开 ready。
  //   phase 2（等它起）：确认停过之后，再等 running && healthy 才算完成。
  //
  // 例外：某些重启快到来不及观察到停止（stop+start 之间端口没断），
  // 所以 phase 1 也带一个 8s 宽限：超时仍未见停止就当作已进入 phase 2。
  useEffect(() => {
    if (!restartingAt) return
    const startedAt = restartingAt
    let alive = true
    let sawDown = false
    const tick = async () => {
      if (!alive) return
      const elapsed = Date.now() - startedAt
      if (elapsed > 150000) {
        if (alive) setRestartingAt(0)
        return
      }
      try {
        const st = await api.freeTierEngineStatus()
        if (!alive) return
        setStatus(st)
        const up = st.running && st.healthy
        if (!up) sawDown = true
        // phase 1：还没确认停过，且未超过 8s 宽限 → 继续等它停
        if (!sawDown && elapsed < 8000) return
        if (up && (sawDown || elapsed >= 8000)) {
          setRestartingAt(0)
          setModels(await api.freeTierModels())
          void reloadRef.current()
        }
      } catch {
        // 连不上 = 引擎已停（phase 1 达成）
        sawDown = true
      }
    }
    void tick()
    const t = window.setInterval(tick, 2000)
    return () => {
      alive = false
      window.clearInterval(t)
    }
  }, [restartingAt])

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
              disabled={busy !== null || restartingAt > 0 || !status?.bin_exists}
              title={status?.bin_exists ? '' : '引擎未安装：请把 opencode2api-win 放入 bin/ 目录'}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
            >
              {busy === 'start' ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
              启动
            </button>
          )}
          <button
            onClick={() => void doAction('restart')}
            disabled={busy !== null || restartingAt > 0 || !status?.bin_exists}
            title={restartingAt > 0 ? '引擎正在重启（WARP 首次注册最长 90s）' : '重启引擎'}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] disabled:opacity-60',
              restartingAt > 0
                ? 'text-amber-800 bg-amber-50 border border-amber-200 cursor-default'
                : 'text-teal-700 bg-teal-50 hover:bg-teal-100',
            )}
          >
            {busy === 'restart' || restartingAt > 0 ? (
              <Loader2 size={14} className="animate-spin" />
            ) : (
              <RefreshCw size={14} />
            )}
            {restartingAt > 0 ? '重启中' : '重启'}
          </button>
          <button
            onClick={() => void doWriteConfig()}
            disabled={busy !== null || restartingAt > 0}
            title="按当前面板设置重新生成引擎 config.json"
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-40"
          >
            {busy === 'config' ? <Loader2 size={14} className="animate-spin" /> : <Settings2 size={14} />}
            重新生成配置
          </button>
        </div>
      </div>

      {/* 通道状态：一眼看出当前走在哪一档（key / WARP / 匿名直连） */}
      <ChannelStatusBar status={status} config={config} />

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

      {/* 可用模型 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex flex-wrap items-center gap-2 mb-3">
          <ListChecks size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">可用模型</h3>
          <label
            className={clsx(
              'ml-2 inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md border text-[12px] cursor-pointer select-none',
              freeOnly
                ? 'border-teal-300 bg-teal-50 text-teal-800'
                : 'border-zinc-200 bg-white text-zinc-600 hover:bg-zinc-50',
              (savingFreeOnly || restartingAt > 0) && 'opacity-70 pointer-events-none cursor-default',
            )}
            title={
              restartingAt > 0
                ? '引擎正在重启，切换完成后再操作（free_only 只在引擎启动时读取）'
                : '写引擎 models.free_only：只暴露免费模型。判定标准 = 模型 ID 含 free，或元数据显示输入/输出成本均为 0 且未废弃'
            }
          >
            <input
              type="checkbox"
              className="accent-teal-600"
              checked={freeOnly}
              disabled={savingFreeOnly || restartingAt > 0}
              onChange={(e) => void toggleFreeOnly(e.target.checked)}
            />
            只使用免费模型
            {(savingFreeOnly || restartingAt > 0) && <Loader2 size={11} className="animate-spin" />}
          </label>
          <span className="flex-1" />
          {restartingAt > 0 && (
            <span className="inline-flex items-center gap-1 text-[11px] text-amber-700 bg-amber-50 border border-amber-200 rounded-md px-2 py-0.5">
              <Loader2 size={10} className="animate-spin" />
              引擎重启中，完成后自动刷新
            </span>
          )}
          <span className="text-[12px] text-zinc-400">
            {models
              ? freeOnly
                ? `${models.free_shown} 免费可用 / 共 ${models.free_total} 免费`
                : `${models.shown} 可用 / 共 ${models.total}`
              : '—'}
          </span>
          <button
            onClick={() => setShowAllModels((v) => !v)}
            className="px-2.5 py-1 rounded-md text-[12px] text-zinc-600 hover:bg-zinc-100"
          >
            {showAllModels ? '只看可用' : '显示全部'}
          </button>
        </div>

        {/* 重启窗口内引擎端口是关的，freeTierModels 会返回 transport 错误。
            那不是故障，只是「还没起来」—— 显示原始 dial tcp 文案只会让人以为坏了。 */}
        {restartingAt > 0 ? (
          <div className="rounded-lg bg-amber-50 border border-amber-200 px-3 py-2 text-[12px] text-amber-900">
            <span className="inline-flex items-center gap-1.5">
              <Loader2 size={12} className="animate-spin" />
              引擎重启中，模型列表将在就绪后自动刷新（WARP 首次注册最长 90s）
            </span>
          </div>
        ) : models?.error ? (
          <div className="rounded-lg bg-zinc-50 border border-zinc-200 px-3 py-2 text-[12px] text-zinc-600">
            {models.error}
          </div>
        ) : !models || models.models.length === 0 ? (
          <div className="text-[12px] text-zinc-400">暂无模型数据（引擎未启动或目录尚未刷新）</div>
        ) : (
          <>
            <div className="overflow-hidden rounded-xl border border-zinc-100">
              <table className="w-full text-[12px]">
                <thead className="bg-zinc-50 text-zinc-500">
                  <tr>
                    <th className="text-left font-medium px-3 py-2">模型</th>
                    <th className="text-left font-medium px-3 py-2 w-16">免费</th>
                    <th className="text-left font-medium px-3 py-2 w-20">状态</th>
                    <th className="text-left font-medium px-3 py-2 w-24">通道</th>
                    <th className="text-left font-medium px-3 py-2">最近结果</th>
                    <th className="text-right font-medium px-3 py-2 w-20">上下文</th>
                    <th className="text-left font-medium px-3 py-2 w-32">下次探测</th>
                    <th className="text-center font-medium px-3 py-2 w-28">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {(showAllModels ? models.models : models.models.filter((m) => m.available))
                    // 「只使用免费模型」勾上时，列表也只留免费的那些 —— 否则
                    // 勾选只改了引擎侧路由，面板里照样混着付费模型，用户会以为
                    // 开关没生效。showAllModels 只控制「是否连不可用的也显示」，
                    // 不越过免费过滤。
                    .filter((m) => !freeOnly || m.free)
                    .slice(0, 200)
                    .map((m) => (
                      <tr key={m.id} className="border-t border-zinc-100">
                        <td className="px-3 py-1.5 font-mono text-zinc-800">{m.id}</td>
                        <td className="px-3 py-1.5">
                          {m.free ? (
                            <span
                              className={clsx(
                                'inline-flex px-1.5 py-0.5 rounded text-[11px] font-medium',
                                m.deprecated ? 'bg-amber-50 text-amber-700' : 'bg-teal-50 text-teal-700',
                              )}
                              title={m.deprecated ? '免费但已废弃' : '免费模型'}
                            >
                              {m.deprecated ? '免费·废弃' : '免费'}
                            </span>
                          ) : (
                            <span className="text-[11px] text-zinc-400">付费</span>
                          )}
                        </td>
                        <td className="px-3 py-1.5">
                          <span
                            className={clsx(
                              'inline-flex px-1.5 py-0.5 rounded text-[11px] font-medium',
                              m.available ? 'bg-green-50 text-green-700' : 'bg-zinc-100 text-zinc-500',
                            )}
                          >
                            {m.available ? '可用' : '不可用'}
                          </span>
                        </td>
                        <td className="px-3 py-1.5 text-zinc-500">{m.channel || '—'}</td>
                        <td className="px-3 py-1.5">
                          <span
                            className={clsx(
                              'font-mono text-[11px]',
                              m.reason === 'probe_succeeded' ? 'text-green-700' : 'text-amber-700',
                            )}
                          >
                            {m.reason || '未探测'}
                          </span>
                        </td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-zinc-500 text-[11px]">
                          {fmtCtx(m.context_window)}
                        </td>
                        <td className="px-3 py-1.5 text-zinc-400 text-[11px]">
                          {fmtRelTime(m.next_check)}
                        </td>
                        <td className="px-3 py-1.5 text-center">
                          {/* 探测 + 状态码做成**一个**固定宽度的胶囊，整体居中。
                              早先是「按钮 + 44px 空徽标槽」两个元素一起居中，
                              空槽把按钮挤到左边 ~25px，没探测过的行看起来像左对齐；
                              探测后徽标填满空槽才显得正常。现在状态码放进胶囊内，
                              宽度固定，所有行视觉一致。 */}
                          <button
                            onClick={() => void probeOne(m.id)}
                            // 只禁「本行正在探测」与「引擎不健康」；其它行照常可点
                            disabled={probingModels.has(m.id) || !status?.healthy}
                            title="对引擎发一次最小请求，看真实状态码"
                            className="inline-flex items-center justify-center gap-1 w-[6.5rem] px-2 py-0.5 rounded text-[11px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
                          >
                            {probingModels.has(m.id) ? (
                              <Loader2 size={11} className="animate-spin" />
                            ) : (
                              <Activity size={11} />
                            )}
                            <span>探测</span>
                            {(() => {
                              const r = probeResults[m.id]
                              if (!r) return null
                              return (
                                <span
                                  className={clsx(
                                    'ml-0.5 px-1 rounded text-[10px] font-mono font-medium',
                                    r.ok
                                      ? 'bg-green-100 text-green-700'
                                      : 'bg-amber-100 text-amber-700',
                                  )}
                                >
                                  {r.status || 'ERR'}
                                </span>
                              )
                            })()}
                          </button>
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
            {/* 过滤后为空要说清楚原因，空白表会被当成加载失败 */}
            {(showAllModels ? models.models : models.models.filter((m) => m.available)).filter(
              (m) => !freeOnly || m.free,
            ).length === 0 && (
              <div className="mt-2 rounded-lg bg-zinc-50 border border-zinc-200 px-3 py-2 text-[12px] text-zinc-600">
                {freeOnly
                  ? `当前没有可显示的免费模型（已勾选「只使用免费模型」）。可点「显示全部」查看 ${models.total} 个模型，或取消勾选以启用付费模型。`
                  : '当前没有可用模型（引擎可能正在刷新目录，或全部模型探测失败）。'}
              </div>
            )}
            {models.models.length > 60 && (
              <div className="mt-2 text-[11px] text-zinc-400">仅显示前 200 条（共 {models.total}）</div>
            )}
            {models.availability_source && (
              <div className="mt-2 text-[10px] text-zinc-300 font-mono truncate" title={models.availability_source}>
                数据源：{models.availability_source}
              </div>
            )}
          </>
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

/**
 * ChannelStatusBar 当前通道状态条。
 *
 * v2.0 起通道是「key 优先 → WARP → 匿名直连」三级降级，用户最关心的是
 * 「现在到底走的哪一档、为什么」。这里把三种依据摊开：
 *   1. 有没有配 key（配了就走 key 通道，匿名被引擎让位）
 *   2. WARP 出口池状态（active / cooling）
 *   3. 实际可用模型数（10 表示匿名可用，0 表示彻底不通）
 */
function ChannelStatusBar({
  status,
  config,
}: {
  status: FreeTierEngineStatus | null
  config: ConfigView | null
}) {
  const zenCount = (config?.engine_zen_keys ?? []).filter((k) => k.trim()).length
  const goCount = (config?.engine_go_keys ?? []).filter((k) => k.trim()).length
  const keyCount = zenCount + goCount

  const warpReady = status?.warp_status === 'ready' && (status?.warp_active ?? 0) > 0
  const warpCoolingOnly = (status?.warp_cooling ?? 0) > 0 && !warpReady

  // 判定当前生效通道
  let lane: 'key' | 'warp' | 'anonymous' | 'down'
  let laneLabel: string
  let laneHint: string
  let cls: string
  if (keyCount > 0 && status?.healthy) {
    lane = 'key'
    laneLabel = `密钥通道（${keyCount} 个 key）`
    laneHint = '已配 key，匿名通道自动让位；key 会均匀绑定到各出口'
    cls = 'bg-teal-50 text-teal-800 border-teal-200'
  } else if (warpReady) {
    lane = 'warp'
    laneLabel = `WARP 出口（活跃 ${status?.warp_active ?? 0}）`
    laneHint = '未配 key，走匿名通道 + WARP 轮换出口'
    cls = 'bg-blue-50 text-blue-800 border-blue-200'
  } else if (warpCoolingOnly) {
    lane = 'down'
    laneLabel = '出口全部冷却'
    laneHint = `${status?.warp_cooling ?? 0} 个出口在冷却，推理请求会挂起；等待恢复或添加 key`
    cls = 'bg-amber-50 text-amber-800 border-amber-200'
  } else if (status?.healthy) {
    lane = 'anonymous'
    laneLabel = '匿名直连'
    laneHint = '未配 key 且 WARP 未启用，全靠本机 IP（易 429）'
    cls = 'bg-zinc-50 text-zinc-700 border-zinc-200'
  } else {
    lane = 'down'
    laneLabel = '不可用'
    laneHint = status?.error || '引擎未运行'
    cls = 'bg-red-50 text-red-700 border-red-200'
  }

  return (
    <div className={clsx('rounded-xl border px-4 py-3 flex items-start gap-3', cls)}>
      <div className="mt-0.5">
        {lane === 'down' ? <AlertCircle size={16} /> : <ShieldCheck size={16} />}
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-[13px] font-semibold">当前通道：{laneLabel}</span>
          <span className="text-[11px] opacity-70">
            模型 {status?.model_shown ?? 0}/{status?.model_total ?? 0}
          </span>
          {warpCoolingOnly && (
            <span className="text-[11px] px-1.5 py-0.5 rounded bg-amber-100">
              冷却 {status?.warp_cooling ?? 0}
            </span>
          )}
        </div>
        <div className="text-[11px] opacity-70 mt-0.5">{laneHint}</div>
      </div>
    </div>
  )
}
