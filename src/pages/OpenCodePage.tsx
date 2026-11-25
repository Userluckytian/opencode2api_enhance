import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import {
  Activity,
  AlertCircle,
  Copy,
  ListChecks,
  Loader2,
  Play,
  Power,
  RefreshCw,
  ShieldCheck,
  Zap,
} from 'lucide-react'
import { api, type FreeTierEngineStatus, type FreeTierModelsView, type ConfigView } from '../lib/api'
import NodePoolCard from '../components/NodePoolCard'
import WarpPoolCard from '../components/WarpPoolCard'

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

  // 密钥展示态：默认遮罩；眼睛图标查看完整值，铅笔图标才进入可改状态（防误触）

  // 可用模型列表（合并引擎 /v1/models 与 availability 探测结果）
  const [models, setModels] = useState<FreeTierModelsView | null>(null)
  const [showAllModels, setShowAllModels] = useState(false)
  // 「只使用免费模型」：写 engine_free_only → 引擎 models.free_only
  const [freeOnly, setFreeOnly] = useState(true)
  const [freeOnlyDirty, setFreeOnlyDirty] = useState(false)
  const [savingFreeOnly, setSavingFreeOnly] = useState(false)
  // restartingAt 非 0 = 本页刚点过重启（乐观标记，驱动 2s 轮询等它重新就绪）。
  // 后端 status.restarting 覆盖「别人触发的重启」（节点池应用、看门狗），
  // 两者取或在 engineRestarting 里合并。
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
    Record<string, { ok: boolean; status: number; retryAfterAt?: string }>
  >({})

  // 付费密钥草稿（zen = 免费层 v1 通道；go = Go 订阅通道）

  // WARP 参数草稿

  // 通道顺序（warp_first / key_first）：决定引擎先走 WARP 出口还是先走 key。
  // 初值 key_first：后端返回真实值前不预设新语义（旧版后端跑的就是这个）。
  // 出口冷却账本：引擎没在 WARP 模式跑时 /healthz 里没有 warp 字段，
  // 「冷却中」会恒显示 0，而账本里压着几十个已烧掉的出口前缀 —— 真相只在这里。

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
      // 免费开关以服务端为准（勾选后会有一次重启，期间别被本地态覆盖）
      if (!freeOnlyDirty) setFreeOnly(c.engine_free_only !== false)
    } catch {
      /* 配置读取失败不阻断状态展示 */
    } finally {
      if (aliveRef.current) setLoading(false)
    }
  }, [freeOnlyDirty])

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

  // 已放弃「重新生成配置」：引擎配置由面板改配置时自动落盘。


  // switchChannelOrder 切换通道顺序（WARP 优先 ↔ 密钥优先）。
  //
  // 为什么切换后必须重启引擎：这个开关决定引擎 config.json 里的 anonymous
  // 字段，而引擎**只在启动时读一次** config.json（没有热更新接口）。
  // 不重启的话，用户点了开关、面板显示变了，引擎却还在按旧顺序跑 —— 静默失败。
  //
  // 重启走 async：WARP 首次出口注册最长 90s，同步等会把一次点击变成转圈一分半。
  // resetCooldown 清空 WARP 出口冷却账本。
  //
  // 只清本地出口账本，**不影响上游账号额度**（额度在服务端，重启也重置不了）。
  // 清完必须重启引擎才会重新注册出口 —— 引擎只在启动时读这份账本。
  // retryWarp 重试 WARP：清除自动降级判定 + 重新启用 WARP 并重启引擎。
  //
  // 用于「池空被自动降级为直连」之后想再试一次（接上 IPv6 / 换网络环境）。
  // 不清冷却账本：那些 /64 确实被上游拒过，清了也是白清。
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
        setProbeResults((prev) => ({
          ...prev,
          [modelId]: { ok: first.ok, status: first.status, retryAfterAt: first.retry_after_at },
        }))
      }
      if (r.note) toast(r.note, false)
      else if (first && !first.ok && first.status === 429 && first.retry_after_at) {
        // 429 不是「稍后重试」这么简单：免费额度耗尽时上游给的是以小时计的
        // 重置时间。不把这句说出来，用户只会反复点探测、反复重启。
        toast(
          `${modelId} → 429 免费额度已用尽，约 ${fmtRelTime(first.retry_after_at)}重置（${first.retry_after_at}）`,
          false,
        )
      } else toast(`${modelId} → ${first?.status ?? 'ERR'}（${first?.latency_ms ?? 0}ms）`, first?.ok)
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

  const [stateCls, stateText] = stateBadge(status?.state ?? 'stopped', status?.running ?? false)
  // 重启状态的唯一来源：本地乐观标记（本页点完立刻生效）或后端 restarting
  // （节点池「应用」、看门狗、其它入口触发的重启也能被看到）。两者取或，
  // 就不会再出现「下面在重启、上面按钮还能再点一次」。
  const engineRestarting = restartingAt > 0 || Boolean(status?.restarting)
  // 出口注册进度：目标 / 就绪 / 预注册 / 冷却。
  const warpTarget = status?.warp_size ?? 0
  const warpReady = status?.warp_active ?? 0
  const warpPreload = status?.warp_preload ?? 0
  const warpCooling = status?.warp_cooling ?? 0
  const warpPhase = engineRestarting
    ? `引擎重启中${warpTarget > 0 ? ` · 出口 ${warpReady}/${warpTarget}` : ''}`
    : status?.state === 'starting'
      ? `引擎启动中${warpPreload > 0 ? ` · 预注册 ${warpPreload} 个出口` : ''}`
      : warpTarget > 0 && warpReady < warpTarget
        ? `出口注册中 · ${warpReady}/${warpTarget}${warpCooling > 0 ? ` · 冷却 ${warpCooling}` : ''}`
        : ''
  const noUsableExit = !engineRestarting && status?.state === 'ready' && warpReady === 0 && warpCooling > 0
  return (
    <div className="p-6 flex flex-col gap-4">
      {/* 工具条 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Zap size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">OpenCode</h1>
          <span
            className={clsx(
              'flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium',
              engineRestarting || status?.state === 'starting'
                ? 'bg-amber-50 text-amber-700'
                : stateCls,
            )}
          >
            <span
              className={clsx(
                'w-1.5 h-1.5 rounded-full',
                engineRestarting || status?.state === 'starting'
                  ? 'bg-amber-500 animate-pulse'
                  : status?.running
                    ? 'bg-green-500'
                    : 'bg-zinc-400',
              )}
            />
            {engineRestarting ? '重启中' : status?.state === 'starting' ? '启动中' : stateText}
          </span>
          {warpTarget > 0 ? (
            <span className="px-2 py-0.5 rounded-full bg-teal-50 text-teal-700 text-xs font-medium">
              出口 {warpReady}/{warpTarget}
              {warpCooling > 0 ? ` · 冷却 ${warpCooling}` : ''}
            </span>
          ) : null}
          {warpPhase ? (
            <span className="text-[11px] text-amber-700">{warpPhase}</span>
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
              disabled={busy !== null || engineRestarting || !status?.bin_exists}
              title={status?.bin_exists ? '' : '引擎未安装：请把 opencode2api-win 放入 bin/ 目录'}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
            >
              {busy === 'start' ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
              启动
            </button>
          )}
          <button
            onClick={() => void doAction('restart')}
            disabled={busy !== null || engineRestarting || !status?.bin_exists}
            title={engineRestarting ? '引擎正在重启（WARP 首次出口注册最长 90s），请等待完成' : '重启引擎'}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] disabled:opacity-60',
              engineRestarting
                ? 'text-amber-800 bg-amber-50 border border-amber-200 cursor-default'
                : 'text-teal-700 bg-teal-50 hover:bg-teal-100',
            )}
          >
            {busy === 'restart' || engineRestarting ? (
              <Loader2 size={14} className="animate-spin" />
            ) : (
              <RefreshCw size={14} />
            )}
            {engineRestarting ? '重启中' : '重启'}
          </button>
        </div>
      </div>

      {/* 通道状态：一眼看出当前走在哪一档（WARP 优先 / 密钥优先 / 不可用） */}
      <div className="order-1">
        <ChannelStatusBar status={status} config={config} />
      </div>

      {/* 池子里一个可用出口都没有时，把「为什么」和「怎么办」直接写出来：
          路由层此时会对每条请求返回 503，不做解释的话用户只能反复重启/点探测。 */}
      {noUsableExit ? (
        <div className="rounded-lg bg-amber-50 border border-amber-200 px-3 py-2 text-[12px] text-amber-900">
          当前没有可用出口（冷却 {warpCooling} 个）：请求会立即返回 503（出口被封 / 无可用出口）。
          处理：① 等冷却到期；② 换出口代理或注册源后重启引擎，重新拿 /64；
          ③ 开启下面「WARP 注册节点池」，让注册轮换走不同出口。
        </div>
      ) : null}

      {/* WARP 注册节点池（订阅 → 本地端口 → engine_warp_registration_proxies） */}
      <div className="order-4">
        <NodePoolCard
          toast={toast}
          // 应用会触发引擎重启：立刻把本页切到「重启中」，顶部按钮同步禁用。
          onEngineRestarting={() => setRestartingAt(Date.now())}
          engineRestarting={engineRestarting}
          engineStatus={status}
        />
      </div>

      {/* 免费层出口池（WARP）：从设置页整体迁入 */}
      <div className="order-5">
        <WarpPoolCard
          toast={toast}
          status={status}
          engineRestarting={engineRestarting}
        />
      </div>


      {/* 引擎接入卡（沿用「统一网关」卡的信息层级） */}
      <div className="order-2 bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
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
      <div className="order-3 bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex flex-wrap items-center gap-2 mb-3">
          <ListChecks size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">可用模型</h3>
          <label
            className={clsx(
              'ml-2 inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md border text-[12px] cursor-pointer select-none',
              freeOnly
                ? 'border-teal-300 bg-teal-50 text-teal-800'
                : 'border-zinc-200 bg-white text-zinc-600 hover:bg-zinc-50',
              (savingFreeOnly || engineRestarting) && 'opacity-70 pointer-events-none cursor-default',
            )}
            title={
              engineRestarting
                ? '引擎正在重启，切换完成后再操作（free_only 只在引擎启动时读取）'
                : '写引擎 models.free_only：只暴露免费模型。判定标准 = 模型 ID 含 free，或元数据显示输入/输出成本均为 0 且未废弃'
            }
          >
            <input
              type="checkbox"
              className="accent-teal-600"
              checked={freeOnly}
              disabled={savingFreeOnly || engineRestarting}
              onChange={(e) => void toggleFreeOnly(e.target.checked)}
            />
            只使用免费模型
            {(savingFreeOnly || engineRestarting) && <Loader2 size={11} className="animate-spin" />}
          </label>
          <span className="flex-1" />
          {engineRestarting && (
            <span className="inline-flex items-center gap-1 text-[11px] text-amber-700 bg-amber-50 border border-amber-200 rounded-md px-2 py-0.5">
              <Loader2 size={10} className="animate-spin" />
              引擎重启中，完成后自动刷新
            </span>
          )}
          <span className="text-[12px] text-zinc-400">
            {models
              ? freeOnly
                ? `${models.usable_shown} 真可用 / 引擎暴露 ${models.free_shown} / 共 ${models.free_total} 免费`
                : `${models.usable_shown} 真可用 / 引擎暴露 ${models.shown} / 共 ${models.total}`
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
        {engineRestarting ? (
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
                    {/* 「状态」= 真实探测结论，不再拿「引擎列不列它」冒充可用 */}
                    <th className="text-left font-medium px-3 py-2 w-20">状态</th>
                    <th className="text-left font-medium px-3 py-2 w-24">通道</th>
                    <th className="text-left font-medium px-3 py-2">最近探测</th>
                    <th className="text-left font-medium px-3 py-2 w-32">额度重置</th>
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
                          {/* 只有「探测通过」才叫可用。引擎照常列出的 429 模型
                              以前被显示成「可用」，用户点探测却是 429，误导性极强。 */}
                          {m.probe_ok ? (
                            <span className="inline-flex px-1.5 py-0.5 rounded text-[11px] font-medium bg-green-50 text-green-700">
                              可用
                            </span>
                          ) : m.available ? (
                            <span
                              className="inline-flex px-1.5 py-0.5 rounded text-[11px] font-medium bg-amber-50 text-amber-700"
                              title="引擎仍把它列在 /v1/models 里，但最近一次探测没有通过"
                            >
                              受限
                            </span>
                          ) : (
                            <span className="inline-flex px-1.5 py-0.5 rounded text-[11px] font-medium bg-zinc-100 text-zinc-500">
                              不可用
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-1.5 text-zinc-500">{m.channel || '—'}</td>
                        <td className="px-3 py-1.5">
                          <span
                            className={clsx(
                              'font-mono text-[11px]',
                              m.probe_ok ? 'text-green-700' : m.reason ? 'text-amber-700' : 'text-zinc-400',
                            )}
                          >
                            {m.reason === 'probe_succeeded'
                              ? '通过'
                              : m.reason
                                ? m.reason.replace(/^http_/, 'HTTP ')
                                : '未探测'}
                          </span>
                        </td>
                        <td className="px-3 py-1.5 text-[11px]">
                          {/* 重置时刻只存在于上游 429 的响应头里，而引擎的
                              availability.json 不落这个字段 —— 所以只有「手动探测过
                              且命中 429」的行才知道确切时刻。其余 429 行给一句引导，
                              比干巴巴一个「—」有用。 */}
                          {m.retry_after_at ? (
                            <span className="text-amber-700" title={`额度重置于 ${m.retry_after_at}`}>
                              {fmtRelTime(m.retry_after_at)}
                            </span>
                          ) : m.reason === 'http_429' ? (
                            <span className="text-zinc-400" title="点「探测」可查询上游给出的重置时刻">
                              点探测查询
                            </span>
                          ) : (
                            <span className="text-zinc-400">—</span>
                          )}
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
                                  title={
                                    r.retryAfterAt
                                      ? `额度重置约 ${fmtRelTime(r.retryAfterAt)}（${r.retryAfterAt}）`
                                      : undefined
                                  }
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



    </div>
  )
}

/**
 * ChannelStatusBar 当前通道状态条。
 *
 * v2.2 起通道顺序可配（warp_first / key_first），状态条必须**按真实顺序**说话，
 * 不能沿用「配了 key 就是密钥通道」那套写死的判定 —— 那样面板会显示
 * 「密钥通道」而引擎其实在走 WARP 匿名通道，用户据此做的判断全错。
 *
 * 判定依据：
 *   1. 通道顺序配置（决定引擎把谁排在前面）
 *   2. WARP 出口池状态（active / cooling）
 *   3. 引擎是否健康
 */
function ChannelStatusBar({
  status,
  config,
}: {
  status: FreeTierEngineStatus | null
  config: ConfigView | null
}) {

  const warpReady = status?.warp_status === 'ready' && (status?.warp_active ?? 0) > 0
  const warpCoolingOnly = (status?.warp_cooling ?? 0) > 0 && !warpReady

  // 判定当前生效通道
  let lane: 'key' | 'warp' | 'fallback' | 'down'
  let laneLabel: string
  let laneHint: string
  let cls: string
  if (!status?.healthy) {
    lane = 'down'
    laneLabel = '不可用'
    laneHint = status?.error || '引擎未运行'
    cls = 'bg-red-50 text-red-700 border-red-200'
  } else if (warpReady) {
    // 出口可用：匿名通道走 WARP 出口。
    lane = 'warp'
    laneLabel = `WARP 出口（活跃 ${status?.warp_active ?? 0}）`
    laneHint = '匿名通道走 WARP 出口；出口被上游限流会自动冷却并替换'
    cls = 'bg-blue-50 text-blue-800 border-blue-200'
  } else if (warpCoolingOnly) {
    lane = 'down'
    laneLabel = '出口全部冷却'
    // 别写成「请求全挂」：WARP 不可用时引擎会退到出口代理列表（通常是直连）走匿名通道。
    laneHint = `${status?.warp_cooling ?? 0} 个出口在冷却；引擎会退到出口代理列表（通常是直连）走匿名通道，可用「重置出口冷却」清掉本地账本后重启引擎`
    cls = 'bg-amber-50 text-amber-800 border-amber-200'
  } else {
    // WARP 出口未就绪：匿名通道走出口代理/本机 IP。
    // 2.0.14 起匿名通道不再熔断（阈值 0）：失败会立刻返回真实原因，
    // 出口恢复即可用，不会再出现「恢复后还要干等一小时」的黑洞。
    lane = 'fallback'
    laneLabel = '匿名直连'
    const breakerThreshold = config?.engine_anonymous_backoff_threshold ?? 0
    laneHint =
      breakerThreshold > 0
        ? `WARP 出口未就绪：先走匿名通道（出口代理 / 本机 IP）；连续失败达 ${breakerThreshold} 次后匿名通道熔断（1h 起，逐次翻倍至 24h），期间无 key 兜底，请求会直接失败`
        : 'WARP 出口未就绪：先走匿名通道（出口代理 / 本机 IP）；匿名通道不熔断，出口恢复后立即恢复，失败会返回具体原因'
    cls = 'bg-amber-50 text-amber-800 border-amber-200'
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
