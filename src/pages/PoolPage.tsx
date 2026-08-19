import { memo, useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { Copy, Loader2, Power, RefreshCw, ShieldCheck, Network, Search, Play, Square, TestTube2, Trash2, KeyRound, Pencil, Check, X, Activity, Settings2, ChevronDown, Layers } from 'lucide-react'
import { api, type GatewayStatus, type Instance, type TestResult, type PoolQualitySummary, type PoolQualityRecord, type PoolQualityLevel, type AutoModelConfig } from '../lib/api'

function statusBadge(st: Instance['status']): [string, string] {
  if (st === 'Running') return ['bg-green-50 text-green-700', '健康']
  if (st === 'Stopped') return ['bg-zinc-100 text-zinc-500', '已停止']
  if (st === 'Starting' || st === 'Stopping') return ['bg-amber-50 text-amber-700', st === 'Starting' ? '启动中' : '停止中']
  if (st && typeof st === 'object' && 'Error' in st) return ['bg-red-50 text-red-600', `错误:${(st as { Error: string }).Error}`]
  return ['bg-zinc-100 text-zinc-500', '未知']
}

// M10: memo 包裹——props（toast/onRelease/onTask 已稳定化）引用不变，App 任务面板变化不重渲染本页大表格
export default memo(function PoolPage({
  toast,
  onRelease,
  onTask,
}: {
  toast: (msg: string, ok?: boolean) => void
  onRelease: (r: { active: boolean; done: number; total: number }) => void
  /** V2: 上报全局任务悬浮窗（restart / batch 进度） */
  onTask: (t: { id: string; type: 'restart' | 'batch'; title: string; done: number; total: number; busy?: boolean; error?: boolean }) => void
}) {
  const [gw, setGw] = useState<GatewayStatus | null>(null)
  const [instances, setInstances] = useState<Instance[]>([])
  // 链路质量（P1 探活评分，按实例名匹配）
  const [quality, setQuality] = useState<PoolQualitySummary | null>(null)
  const [qualityBusy, setQualityBusy] = useState(false)
  // 性能模式开关（P2 质量加权路由 + 熔断）
  const [perfMode, setPerfMode] = useState<boolean | null>(null)
  // S4: 池成员勾选——批量操作按勾选集作用；未勾选时保持"全部"行为
  const [poolSelected, setPoolSelected] = useState<Set<string>>(new Set())
  const toggleSelected = (name: string) =>
    setPoolSelected((prev) => {
      const n = new Set(prev)
      if (n.has(name)) n.delete(name)
      else n.add(name)
      return n
    })
  // 性能模式参数（P1/P2/P2b/D3）：探活间隔/窗口 + 熔断/半开 + 并发
  const [poolForm, setPoolForm] = useState({
    pool_probe_interval_sec: 45,
    pool_quality_window_min: 10,
    pool_breaker_threshold: 3,
    pool_halfopen_interval_sec: 60,
    pool_race_copies: 2,
    scan_concurrency: 8,
    batch_concurrency: 4,
    test_concurrency: 4,
    pool_probe_concurrency: 4,
  })
  const [poolProbeEnabled, setPoolProbeEnabled] = useState(true)
  // 网关超时切换区间（实例池/统一网关请求行为）
  const [timeoutForm, setTimeoutForm] = useState({
    timeout_ttft_min_ms: 10000,
    timeout_ttft_max_ms: 10000,
    timeout_silence_min_ms: 5000,
    timeout_silence_max_ms: 5000,
    failover_probe_min: 2,
    failover_probe_max: 3,
    call_log_max: 5000,
  })
  // 节点前缀展示开关（默认关闭）
  const [showNodePrefix, setShowNodePrefix] = useState(false)
  // 测试结果（行内徽章正反馈）：name → TestResult
  const [testResults, setTestResults] = useState<Record<string, TestResult>>({})
  const [stopping, setStopping] = useState(false)
  const [routeBusy, setRouteBusy] = useState(false)
  const [kickBusy, setKickBusy] = useState<string | null>(null)
  // P2 audit: 工具条「刷新」/ 路由模式切换目标 / 性能模式/节点前缀开关切身忙态
  const [refreshing, setRefreshing] = useState(false)
  const [routeTarget, setRouteTarget] = useState<'smart' | 'failover' | 'round_robin' | null>(null)
  const [perfBusy, setPerfBusy] = useState(false)
  const [prefixBusy, setPrefixBusy] = useState(false)
  // P2 audit: 设置弹窗三个「保存」按钮忙态
  const [savingPool, setSavingPool] = useState(false)
  const [savingTimeout, setSavingTimeout] = useState(false)
  const [savingUi, setSavingUi] = useState(false)
  const [search, setSearch] = useState('')
  const [searchFocus, setSearchFocus] = useState(false)
  // 状态筛选（U2，对齐独享页）：全部 / 运行中 / 已停止
  const [filter, setFilter] = useState<'all' | 'running' | 'stopped'>('all')
  // 单行操作忙态；全部操作忙态（start / stop / test）
  const [rowBusy, setRowBusy] = useState<Record<string, 'start' | 'stop' | 'test'>>({})
  const [allBusy, setAllBusy] = useState<'start' | 'stop' | 'test' | null>(null)
  const [restarting, setRestarting] = useState(false)

  // 统一网关自定义密钥：编辑弹窗（输入新密钥 / 重置默认）
  const [keyOpen, setKeyOpen] = useState(false)
  const [keyValue, setKeyValue] = useState('')
  const [keyBusy, setKeyBusy] = useState(false)
  // 统一网关自定义端口（0 = 未设置，用环境槽位/默认 40080）
  const [gwPortCfg, setGwPortCfg] = useState(0)
  const [portOpen, setPortOpen] = useState(false)
  const [portValue, setPortValue] = useState('')
  const [portBusy, setPortBusy] = useState(false)
  // 页面设置弹窗（性能模式参数 + 网关超时切换 + 界面刷新，收进右上角齿轮）
  const [settingsOpen, setSettingsOpen] = useState(false)
  // 折叠面板：每个配置分组一个 section，默认收起
  const [openSections, setOpenSections] = useState<{ perf: boolean; timeout: boolean; auto: boolean; ui: boolean }>({ perf: false, timeout: false, auto: false, ui: false })
  // auto 虚拟模型（默认关闭）：权重 0~10 + 每模型上下文上限，保存即热生效
  const [autoForm, setAutoForm] = useState<AutoModelConfig>({ enabled: false, strategy: 'balanced' })
  const [savingAuto, setSavingAuto] = useState(false)
  // 界面刷新（U3）：轮询间隔（秒，0 = 关闭轮询）（生效值 + 表单值）
  const [uiPollSec, setUiPollSec] = useState(5)
  const [uiForm, setUiForm] = useState({ ui_poll_interval_sec: 5 })
  // 一键释放全部池成员忙态
  const [releaseAllBusy, setReleaseAllBusy] = useState(false)
  // G25: 释放收尾 timer 句柄 + 代次——快速连续释放时旧 timer 不误关新任务
  const releaseFinishTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const releaseGen = useRef(0)
  // 杂项: 释放防重入——releaseAllBusy 是异步 state，连点两下会在生效前二次进入
  const releaseAllGuard = useRef(false)

  // 池成员 = 已入池（join_gateway=true）的实例；支持前端搜索（名称/节点/IP/端口）+ 状态筛选
  // G30: poolMembers = 未过滤的真成员全集，members = 筛选后可见集——空态区分「真为空」与「筛选无匹配」
  const poolMembers = instances.filter((i) => i.join_gateway)
  const members = poolMembers
    .filter((i) => {
      const q = search.trim().toLowerCase()
      return (
        !q ||
        i.name.toLowerCase().includes(q) ||
        i.node.toLowerCase().includes(q) ||
        (i.ip || '').toLowerCase().includes(q) ||
        String(i.port).includes(q)
      )
    })
    .filter((i) => {
      // 状态筛选：与独享页一致，运行中仅留 Running / 已停止仅留 Stopped
      if (filter === 'running' && i.status !== 'Running') return false
      if (filter === 'stopped' && i.status !== 'Stopped') return false
      return true
    })

  const qualityByName = new Map<string, PoolQualityRecord>()
  if (quality) for (const r of quality.records) qualityByName.set(r.name, r)

  // auto 模型行：网关免费模型（排除 auto 自身）∪ 已配置键（网关未启动时也能编辑既有配置）
  const autoModelRows = Array.from(
    new Set([
      ...(gw?.free_models ?? []).filter((m) => m.toLowerCase() !== 'auto'),
      ...Object.keys(autoForm.weights || {}),
      ...Object.keys(autoForm.context_windows || {}),
    ]),
  ).sort()

  // M9: 轮询代次守卫——load 开始记代，响应后比对，过期响应丢弃（慢响应不叠加、旧快照不覆盖新状态）
  const loadGen = useRef(0)
  const load = useCallback(async () => {
    const gen = ++loadGen.current
    try {
      const [g, ins, q] = await Promise.all([api.gatewayStatus(), api.listInstances(), api.poolQuality()])
      if (gen !== loadGen.current) return
      setGw(g)
      setInstances(ins)
      setQuality(q)
    } catch (e) {
      /* 轮询静默失败，保留上次状态 */
    }
  }, [])

  // P2 audit: 工具条「刷新」显式忙态（spinner + 禁用）
  const doRefresh = async () => {
    setRefreshing(true)
    try {
      await load()
    } finally {
      setRefreshing(false)
    }
  }

  // 性能模式参数初始值（P2）：从配置加载生效默认值，保证输入框有默认填充
  useEffect(() => {
    api
      .configGet()
      .then((c) => {
        setPerfMode(c.pool_performance_mode)
        setPoolProbeEnabled(c.pool_probe_enabled)
        setPoolForm({
          pool_probe_interval_sec: c.pool_probe_interval_sec,
          pool_quality_window_min: c.pool_quality_window_min,
          pool_breaker_threshold: c.pool_breaker_threshold,
          pool_halfopen_interval_sec: c.pool_halfopen_interval_sec,
          pool_race_copies: c.pool_race_copies,
          scan_concurrency: c.scan_concurrency,
          batch_concurrency: c.batch_concurrency,
          test_concurrency: c.test_concurrency,
          pool_probe_concurrency: c.pool_probe_concurrency,
        })
        setTimeoutForm({
          timeout_ttft_min_ms: c.timeout_ttft_min_ms,
          timeout_ttft_max_ms: c.timeout_ttft_max_ms,
          timeout_silence_min_ms: c.timeout_silence_min_ms,
          timeout_silence_max_ms: c.timeout_silence_max_ms,
          failover_probe_min: c.failover_probe_min,
          failover_probe_max: c.failover_probe_max,
          call_log_max: c.call_log_max,
        })
        setShowNodePrefix(c.show_node_prefix)
        setGwPortCfg(c.gateway_port ?? 0)
        setUiForm({ ui_poll_interval_sec: c.ui_poll_interval_sec })
        setUiPollSec(c.ui_poll_interval_sec)
      })
      .catch(() => {})
    api
      .autoModelGet()
      .then((a) => setAutoForm({ enabled: !!a.enabled, strategy: a.strategy || 'balanced', weights: a.weights || {}, context_windows: a.context_windows || {} }))
      .catch(() => {})
  }, [])

  // 首次加载 + 轻量轮询（网关状态 / 实例健康会变化）：间隔取配置 ui_poll_interval_sec（0 = 不轮询，默认 5s）
  useEffect(() => {
    void load()
    if (uiPollSec <= 0) return
    const timer = setInterval(() => void load(), uiPollSec * 1000)
    return () => clearInterval(timer)
  }, [load, uiPollSec])

  const copyText = async (text: string, label: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast(`已复制${label}`)
    } catch {
      /* ignore */
    }
  }

  const doStopGateway = async () => {
    if (!confirm('确定关闭统一网关？实例的入池标记会保留，重新启动后自动恢复。')) return
    setStopping(true)
    try {
      await api.gatewayStop()
      toast('已关闭统一网关')
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setStopping(false)
    }
  }

  const doRestart = async () => {
    if (!confirm('确定一键重启实例池？\n将停止全部实例与网关、强制释放被占用的端口，再启动全部池成员。')) return
    setRestarting(true)
    // V2: 一键重启为单次后端调用、无中间回调——用 0→1 两态进度，完成由 App 自动收起
    onTask({ id: 'restart', type: 'restart', title: '一键重启池', done: 0, total: 1, busy: true })
    try {
      const r = await api.restartPool()
      onTask({ id: 'restart', type: 'restart', title: '一键重启池', done: 1, total: 1, busy: false, error: !!r.error })
      const parts = [`已停止 ${r.stopped} 个`, `启动 ${r.started} 个`]
      if (r.freed_ports.length > 0) parts.push(`强制释放端口 ${r.freed_ports.join(', ')}`)
      parts.push(`网关${r.gateway_running ? '运行中' : '未启动'}`)
      toast(parts.join(' · ') + (r.error ? `（${r.error}）` : ''), !r.error)
      await load()
    } catch (e) {
      onTask({ id: 'restart', type: 'restart', title: '一键重启池', done: 1, total: 1, busy: false, error: true })
      toast(String(e), false)
    } finally {
      setRestarting(false)
    }
  }

  const doSetRouteMode = async (mode: 'smart' | 'failover' | 'round_robin') => {
    if (!gw || gw.route_mode === mode) return
    setRouteTarget(mode)
    setRouteBusy(true)
    try {
      await api.gatewaySetRouteMode(mode)
      const label = mode === 'smart' ? 'smart（默认：故障转移+健康计数+超时切换）' : mode === 'failover' ? 'failover（失败才切换）' : 'round_robin（轮询分发）'
      toast(`已切换路由模式：${label}`)
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setRouteTarget(null)
      setRouteBusy(false)
    }
  }

  // 立即触发一轮链路探活（P1）
  const doProbe = async () => {
    setQualityBusy(true)
    try {
      const q = await api.poolQualityProbe()
      setQuality(q)
      toast(`链路探活完成：healthy ${q.healthy} · degraded ${q.degraded} · flaky ${q.flaky} · down ${q.down}`, q.down === 0)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setQualityBusy(false)
    }
  }

  // 性能模式开关（P2）：关闭时路由行为与基线一致
  const doTogglePerf = async () => {
    const next = !(perfMode ?? true)
    setPerfBusy(true)
    try {
      await api.configSet('pool_performance_mode', String(next))
      setPerfMode(next)
      toast(next ? '性能模式已开启：质量加权路由 + 熔断自动恢复' : '性能模式已关闭：回到基线路由行为', true)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setPerfBusy(false)
    }
  }

  // 保存性能模式参数（P1/P2/P2b/D3）：探活/质量/熔断/半开/并发/探活开关，热生效
  const handleSavePool = async () => {
    const f = poolForm
    if (f.pool_probe_interval_sec < 0 || f.pool_quality_window_min < 1 ||
        f.pool_breaker_threshold < 1 || f.pool_halfopen_interval_sec < 1) {
      toast('性能模式参数不合法：间隔需 ≥0，窗口/熔断/半开需 ≥1', false)
      return
    }
    const concurrency: [string, number, number, number][] = [
      ['竞速并行（1~4）', f.pool_race_copies, 1, 4],
      ['节点扫描并发（1~16）', f.scan_concurrency, 1, 16],
      ['批量启停/释放并发（1~16）', f.batch_concurrency, 1, 16],
      ['一键测试并发（1~16）', f.test_concurrency, 1, 16],
      ['链路探活并发（1~16）', f.pool_probe_concurrency, 1, 16],
    ]
    for (const [label, v, lo, hi] of concurrency) {
      if (v < lo || v > hi) {
        toast(`并发不合法：${label}`, false)
        return
      }
    }
    setSavingPool(true)
    try {
      await api.configSet('pool_probe_interval_sec', String(f.pool_probe_interval_sec))
      await api.configSet('pool_quality_window_min', String(f.pool_quality_window_min))
      await api.configSet('pool_breaker_threshold', String(f.pool_breaker_threshold))
      await api.configSet('pool_halfopen_interval_sec', String(f.pool_halfopen_interval_sec))
      await api.configSet('pool_probe_enabled', String(poolProbeEnabled))
      await api.configSet('pool_race_copies', String(f.pool_race_copies))
      await api.configSet('scan_concurrency', String(f.scan_concurrency))
      await api.configSet('batch_concurrency', String(f.batch_concurrency))
      await api.configSet('test_concurrency', String(f.test_concurrency))
      await api.configSet('pool_probe_concurrency', String(f.pool_probe_concurrency))
      toast('性能模式配置已保存（热生效）', true)
    } catch (e) {
      console.error('保存性能模式配置失败', e)
      toast('保存失败', false)
    } finally {
      setSavingPool(false)
    }
  }

  // auto 虚拟模型保存：权重钳 0~10、上下文仅收正数（空 = 保守默认），保存即热生效
  const handleSaveAuto = async () => {
    const weights: Record<string, number> = {}
    const contextWindows: Record<string, number> = {}
    for (const m of autoModelRows) {
      const w = Math.round(Number(autoForm.weights?.[m] ?? 5))
      weights[m] = Number.isFinite(w) ? Math.min(10, Math.max(0, w)) : 5
      const c = Math.round(Number(autoForm.context_windows?.[m] ?? 0))
      if (Number.isFinite(c) && c > 0) contextWindows[m] = c
    }
    setSavingAuto(true)
    try {
      const res = await api.autoModelSave({
        enabled: autoForm.enabled,
        strategy: autoForm.strategy || 'balanced',
        weights,
        context_windows: contextWindows,
      })
      setAutoForm({
        enabled: res.config.enabled,
        strategy: res.config.strategy || 'balanced',
        weights: res.config.weights || {},
        context_windows: res.config.context_windows || {},
      })
      toast('auto 模型配置已保存（热生效）', true)
      await load()
    } catch (e) {
      console.error('保存 auto 模型配置失败', e)
      toast('保存失败', false)
    } finally {
      setSavingAuto(false)
    }
  }

  // 校验区间：min <= max，且为正数
  const validateRange = (min: number, max: number): boolean => {
    return min > 0 && max >= min
  }

  const handleSaveTimeout = async () => {
    const f = timeoutForm
    if (!validateRange(f.timeout_ttft_min_ms, f.timeout_ttft_max_ms) ||
        !validateRange(f.timeout_silence_min_ms, f.timeout_silence_max_ms) ||
        !validateRange(f.failover_probe_min, f.failover_probe_max)) {
      toast('区间不合法：最小值需 >0 且 最小值 ≤ 最大值', false)
      return
    }
    if (f.call_log_max < 100) {
      toast('日志保留上限至少 100 条', false)
      return
    }
    setSavingTimeout(true)
    try {
      await api.configSet('timeout_ttft_min_ms', String(f.timeout_ttft_min_ms))
      await api.configSet('timeout_ttft_max_ms', String(f.timeout_ttft_max_ms))
      await api.configSet('timeout_silence_min_ms', String(f.timeout_silence_min_ms))
      await api.configSet('timeout_silence_max_ms', String(f.timeout_silence_max_ms))
      await api.configSet('failover_probe_min', String(f.failover_probe_min))
      await api.configSet('failover_probe_max', String(f.failover_probe_max))
      await api.configSet('call_log_max', String(f.call_log_max))
      toast('超时配置已保存（重启网关后生效）', true)
    } catch (e) {
      console.error('保存超时配置失败', e)
      toast('保存失败', false)
    } finally {
      setSavingTimeout(false)
    }
  }

  // 界面刷新（U3）：保存轮询间隔（0~60，0 = 关闭自动轮询）
  const handleSaveUi = async () => {
    const v = uiForm.ui_poll_interval_sec
    if (!Number.isInteger(v) || v < 0 || v > 60) {
      toast('刷新间隔需为 0~60 的整数（0 = 关闭自动刷新）', false)
      return
    }
    setSavingUi(true)
    try {
      await api.configSet('ui_poll_interval_sec', String(v))
      setUiPollSec(v)
      toast('界面刷新配置已保存（已生效）', true)
    } catch (e) {
      console.error('保存界面刷新配置失败', e)
      toast('保存失败', false)
    } finally {
      setSavingUi(false)
    }
  }

  const handleShowNodePrefixChange = async (enabled: boolean) => {
    setPrefixBusy(true)
    try {
      await api.configSet('show_node_prefix', String(enabled))
      setShowNodePrefix(enabled)
      toast(enabled ? '已开启节点前缀展示' : '已关闭节点前缀展示', true)
    } catch (e) {
      console.error('设置节点前缀失败', e)
      toast('设置失败', false)
    } finally {
      setPrefixBusy(false)
    }
  }

  const doRelease = async (name: string) => {
    if (!confirm(`确定释放实例 ${name}？将关闭实例并释放节点。`)) return
    setKickBusy(name)
    try {
      await api.removeInstance(name)
      toast(`已释放实例 ${name}`)
      // G3: 释放后从勾选集剔除
      setPoolSelected((prev) => {
        const n = new Set(prev)
        n.delete(name)
        return n
      })
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setKickBusy(null)
    }
  }

  // 设置统一网关自定义密钥（≥8 字符；空串 = 重置默认）
  const doSaveKey = async () => {
    setKeyBusy(true)
    try {
      await api.configSet('gateway_key', keyValue.trim())
      toast(keyValue.trim() ? '网关自定义密钥已设置并立即生效' : '网关密钥已重置为默认并立即生效', true)
      setKeyOpen(false)
      setKeyValue('')
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setKeyBusy(false)
    }
  }

  // 设置统一网关自定义端口（1-65535；空串 = 恢复默认槽位端口）
  const doSavePort = async () => {
    const v = portValue.trim()
    if (v !== '' && (Number.isNaN(Number(v)) || !Number.isInteger(Number(v)) || Number(v) < 1 || Number(v) > 65535)) {
      toast('端口必须是 1-65535 的整数，或留空恢复默认', false)
      return
    }
    setPortBusy(true)
    try {
      await api.configSet('gateway_port', v === '' ? '0' : v)
      toast(v === '' ? '网关端口已恢复默认并立即生效' : `网关端口已设为 ${v} 并立即生效`, true)
      setPortOpen(false)
      setPortValue('')
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setPortBusy(false)
    }
  }

  // S3: 释放确认弹窗模式（null=关闭；'all' 完全释放；'running' 仅释放运行中）
  const [releaseMode, setReleaseMode] = useState<'all' | 'running' | null>(null)
  // 批量释放池成员：按所选模式（完全/仅运行中）分块并发删除 + 实时上报进度
  const doReleaseAll = async (mode: 'all' | 'running') => {
    // 防重入：连点两下不并发两组释放循环（releaseAllBusy 是异步 state）
    if (releaseAllGuard.current) return
    // T2: 仅作用于勾选集
    const base = members.filter((i) => poolSelected.has(i.name))
    const targets = mode === 'running' ? base.filter((i) => i.status === 'Running') : base
    setReleaseMode(null)
    if (targets.length === 0) {
      toast(base.length === 0 ? '请先勾选池成员' : mode === 'running' ? '勾选的成员中没有运行中的' : '勾选中暂无成员')
      return
    }
    const names = targets.map((i) => i.name)
    // G25: 新一轮释放开始——作废旧收尾 timer，代次 +1（收尾回调比对代次，不误关新任务）
    if (releaseFinishTimer.current) clearTimeout(releaseFinishTimer.current)
    const releaseGenThis = ++releaseGen.current
    releaseAllGuard.current = true
    setReleaseAllBusy(true)
    onRelease({ active: true, done: 0, total: names.length })
    try {
      let done = 0
      let fail = 0
      const batchSize = 4 // 与后端 BatchDelete 并发一致，避免进程风暴
      for (let i = 0; i < names.length; i += batchSize) {
        const chunk = names.slice(i, i + batchSize)
        const results = await Promise.allSettled(chunk.map((n) => api.removeInstance(n)))
        results.forEach((r) => {
          if (r.status === 'fulfilled') done++
          else fail++
        })
        onRelease({ active: true, done, total: names.length })
      }
      onRelease({ active: true, done: names.length, total: names.length })
      toast(`已释放 ${done} 个成员${fail ? `，失败 ${fail}` : ''}`, fail === 0)
      // G3: 释放后从勾选集剔除（失败项仍保留在列表，可重新勾选）
      setPoolSelected((prev) => {
        const n = new Set(prev)
        names.forEach((x) => n.delete(x))
        return n
      })
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setReleaseAllBusy(false)
      releaseAllGuard.current = false
      // G25: 面板短暂显示"已完成"后自动消失——仅当没有新一轮释放（同代次）时收尾
      releaseFinishTimer.current = setTimeout(() => {
        if (releaseGenThis === releaseGen.current) onRelease({ active: false, done: 0, total: 0 })
      }, 1200)
    }
  }

  // 单行操作：启动 / 停止 / 测试（与实例页行为一致）
  const doRowOp = async (name: string, op: 'start' | 'stop' | 'test') => {
    setRowBusy((prev) => ({ ...prev, [name]: op }))
    try {
      if (op === 'start') {
        await api.startInstance(name)
        toast(`已启动实例 ${name}`)
      } else if (op === 'stop') {
        await api.stopInstance(name)
        toast(`已停止实例 ${name}`)
      } else {
        const r = await api.testInstance(name)
        setTestResults((prev) => ({ ...prev, [name]: r }))
        if (r.ok) toast(`「${name}」测试通过：${r.message}（${r.latency_ms}ms）`)
        else toast(r.message || '测试失败', false)
      }
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setRowBusy((prev) => {
        const next = { ...prev }
        delete next[name]
        return next
      })
    }
  }

  // 批量操作：仅作用于勾选集（T2 纯勾选驱动）
  const doAll = async (kind: 'start' | 'stop' | 'test') => {
    const scope = members.filter((i) => poolSelected.has(i.name))
    const names = scope.map((i) => i.name)
    if (names.length === 0) {
      toast('请先勾选池成员')
      return
    }
    setAllBusy(kind)
    // V2: 批量启停/测试上报 batch 任务（id 按动作区分，可与其它任务并存堆叠）
    const taskId = `batch-${kind}`
    const reportBatch = (done: number, total: number, busy: boolean, error?: boolean) =>
      onTask({ id: taskId, type: 'batch', title: '批量启停', done, total, busy, error })
    reportBatch(0, names.length, true)
    try {
      let ok = 0
      let fail = 0
      let skippedCount = 0
      if (kind === 'start' || kind === 'stop') {
        // 复用 Rust 并行命令（batch_start 4 worker / batch_stop 8 worker），避免前端串行
        const r = kind === 'start' ? await api.batchStart(names) : await api.batchStop(names)
        ok = r.success_count
        fail = r.error_count
        skippedCount = kind === 'start' ? (r.skipped_count ?? 0) : 0
      } else {
        // 测试：仅测试运行中的成员；未启动计入「跳过」，避免误报失败。
        const runningNames = scope.filter((i) => i.status === 'Running').map((i) => i.name)
        const skipped = names.length - runningNames.length
        if (runningNames.length === 0) {
          reportBatch(names.length, names.length, false)
          toast(`池成员均未启动（${names.length} 个），无需测试`, false)
          return
        }
        // 逐条测试完成即上报 done（并发行为不变，仅加计数回调）
        let testDone = 0
        const results = await Promise.allSettled(
          runningNames.map(async (n) => {
            try {
              const r = await api.testInstance(n)
              reportBatch(++testDone, runningNames.length, true)
              return r
            } catch (e) {
              reportBatch(++testDone, runningNames.length, true)
              throw e
            }
          }),
        )
        const updated: Record<string, TestResult> = {}
        runningNames.forEach((n, i) => {
          const r = results[i]!
          if (r.status === 'fulfilled' && r.value.ok) {
            ok++
            updated[n] = r.value
          } else {
            fail++
            updated[n] = {
              name: n,
              port: 0,
              ok: false,
              status_code: null,
              model_count: null,
              message: r.status === 'fulfilled' ? r.value.message : String(r.reason),
              latency_ms: 0,
            }
          }
        })
        setTestResults((prev) => ({ ...prev, ...updated }))
        if (skipped > 0) toast(`已跳过未启动的 ${skipped} 个池成员`, true)
      }
      const label = kind === 'start' ? '启动' : kind === 'stop' ? '停止' : '测试'
      const skippedPart = kind === 'start' && skippedCount > 0 ? `，跳过已运行 ${skippedCount}` : ''
      toast(`池成员${label}完成：成功 ${ok} 个，失败 ${fail} 个${skippedPart}`, fail === 0)
      reportBatch(names.length, names.length, false, fail > 0)
      await load()
    } catch (e) {
      // 关闭任务（失败标记红色）并 toast，避免悬浮窗残留忙态
      reportBatch(names.length, names.length, false, true)
      toast(String(e), false)
    } finally {
      setAllBusy(null)
    }
  }

  const running = gw?.running ?? false
  const freeModels = gw?.free_models ?? []
  const freeModelsError = gw?.free_models_error ?? null
  // T2: 释放确认弹窗基数——仅按勾选集
  const selScope = members.filter((i) => poolSelected.has(i.name))
  const selTotal = selScope.length
  const selRunning = selScope.filter((i) => i.status === 'Running').length
  // G3: 批量按钮计数/禁用口径统一为「可见且选中」集——切筛选或搜索收窄后，隐藏的选中项不计入
  const visibleSelected = new Set(selScope.map((i) => i.name))
  const allChecked = members.length > 0 && visibleSelected.size === members.length

  return (
    <div className="p-6 space-y-4">
      {/* 工具条 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Layers size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">实例池</h1>
          <span
            className={clsx(
              'flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium',
              running ? 'bg-green-50 text-green-700' : 'bg-zinc-100 text-zinc-500',
            )}
          >
            <span className={clsx('w-1.5 h-1.5 rounded-full', running ? 'bg-green-500' : 'bg-zinc-400')} />
            {running ? '网关运行中' : '网关未启动'}
          </span>
          <span className="px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-500 text-xs font-medium">
            {members.length} 个池成员
          </span>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => void doRefresh()}
            disabled={refreshing}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:cursor-not-allowed disabled:opacity-70"
          >
            <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
            {refreshing ? '刷新中…' : '刷新'}
          </button>
          <button
            onClick={() => void doStopGateway()}
            disabled={!running || stopping}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-red-600 bg-red-50 hover:bg-red-100 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {stopping ? <Loader2 size={14} className="animate-spin" /> : <Power size={14} />}
            {stopping ? '关闭中…' : '一键关闭网关'}
          </button>
          <button
            onClick={() => void doRestart()}
            disabled={restarting || members.length === 0}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {restarting ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
            {restarting ? '重启中…' : '一键重启'}
          </button>
          <button
            onClick={() => setSettingsOpen(true)}
            title="实例池设置（性能模式参数 / 网关超时切换 / 界面刷新）"
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
          >
            <Settings2 size={14} /> 设置
          </button>
        </div>
      </div>

      {/* 网关状态卡 */}
      {/* 网关状态卡 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2 mb-4">
          <ShieldCheck size={16} className="text-teal-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">统一网关</h3>
          <span className="flex-1" />
          <span className="text-[12px] text-zinc-400">{gw?.message || '加载中…'}</span>
        </div>

        <div className="grid grid-cols-2 gap-4">
          {/* 地址 */}
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="flex items-center justify-between mb-1.5">
              <div className="text-[12px] text-zinc-500">统一 API 地址</div>
              <button
                onClick={() => { setPortValue(gwPortCfg > 0 ? String(gwPortCfg) : ''); setPortOpen(true) }}
                className="flex items-center gap-1 text-[11px] text-teal-700 hover:underline"
                title="设置自定义端口（立即生效）/ 恢复默认"
              >
                <Pencil size={11} /> 自定义端口
              </button>
            </div>
            <button
              onClick={() => void copyText(gw?.address ?? '', '统一 API 地址')}
              className="flex items-center gap-1 text-teal-700 hover:underline"
              title="点击复制"
            >
              <code className="text-[13px]">{gw?.address ?? (gwPortCfg > 0 ? `http://127.0.0.1:${gwPortCfg}/v1` : import.meta.env.DEV ? 'http://127.0.0.1:44180/v1' : 'http://127.0.0.1:40080/v1')}</code>
              <Copy size={12} />
            </button>
            <div className="mt-1 text-[11px] text-zinc-400">
              池内 {gw?.running_instances ?? 0} / 共 {gw?.total_instances ?? 0} 个运行实例
            </div>
          </div>

          {/* 密钥 */}
          <div className="rounded-xl border border-zinc-100 bg-zinc-50/60 p-4">
            <div className="flex items-center justify-between mb-1.5">
              <div className="text-[12px] text-zinc-500">统一密钥</div>
              <button
                onClick={() => { setKeyValue(''); setKeyOpen(true) }}
                className="flex items-center gap-1 text-[11px] text-teal-700 hover:underline"
                title="设置自定义密钥（立即生效）/ 重置默认"
              >
                <Pencil size={11} /> 自定义
              </button>
            </div>
            <button
              onClick={() => void copyText(gw?.api_key ?? 'sk-unified-local', '统一密钥')}
              className="flex items-center gap-1 text-zinc-600 hover:underline"
              title="点击复制"
            >
              <code className="text-[13px]">{gw?.api_key ?? 'sk-unified-local'}</code>
              <Copy size={12} />
            </button>
            <div className="mt-1 text-[11px] text-zinc-400">配置客户端时使用此地址 + 密钥</div>
          </div>
        </div>

        {/* 免费模型 */}
        <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2">
          <span className="text-[12px] text-zinc-500">网关可用免费模型：</span>
          {gw?.free_models_loading ? (
            <span className="flex items-center gap-1 text-[12px] text-zinc-400">
              <Loader2 size={12} className="animate-spin" /> 探测中…
            </span>
          ) : freeModels.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {freeModels.map((m) => (
                <span key={m} className="px-2 py-0.5 rounded-md bg-teal-50 text-teal-700 text-[11px] font-medium">
                  {m}
                </span>
              ))}
            </div>
          ) : freeModelsError ? (
            <span className="text-[12px] text-red-500">探测失败，{freeModelsError}</span>
          ) : (
            <span className="text-[12px] text-zinc-400">—</span>
          )}
        </div>
      </div>

      {/* 路由模式 + 操作提示 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5 flex items-center justify-between">
        <div>
          <h3 className="text-[14px] font-semibold text-zinc-900 mb-1.5">路由模式</h3>
          <p className="text-[12px] text-zinc-400 space-y-1">
            <span className="block">smart（默认）：{perfMode ? '质量加权 + ' : ''}故障转移 + 健康计数 + 超时切换</span>
            <span className="block">failover：失败才切换</span>
            <span className="block">round_robin：轮询分发</span>
            {perfMode && <span className="block text-teal-600">性能模式：坏节点按质量分自动降权/剔除，熔断到期自动回归</span>}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2" title={perfMode ? '开启：质量加权路由 + 熔断/半开自动恢复' : '关闭：路由行为与基线一致（纯游标+冷却）'}>
            <button
              onClick={() => void doTogglePerf()}
              disabled={perfMode === null || perfBusy}
              className={clsx(
                'relative inline-flex items-center h-6 w-11 rounded-full transition-colors disabled:opacity-50',
                perfMode ? 'bg-teal-600' : 'bg-zinc-300',
              )}
              aria-label="性能模式开关"
            >
              <span className={clsx('inline-block w-4 h-4 rounded-full bg-white shadow transition-transform', perfMode ? 'translate-x-6' : 'translate-x-1')} />
            </button>
            <span className="text-[12px] text-zinc-600 whitespace-nowrap">性能模式</span>
          </div>
          <span className="w-px h-6 bg-zinc-200" />
          <button
            onClick={() => void doProbe()}
            disabled={qualityBusy}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 border border-teal-100 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {qualityBusy ? <Loader2 size={14} className="animate-spin" /> : <Activity size={14} />}
            {qualityBusy ? '探活中…' : '立即探活'}
          </button>
          <div className="flex items-center gap-2">
            {(['smart', 'failover', 'round_robin'] as const).map((m) => (
              <button
                key={m}
                onClick={() => void doSetRouteMode(m)}
                disabled={!running || routeBusy}
                className={clsx(
                  'px-4 py-1.5 rounded-lg text-[13px] border transition-colors disabled:cursor-not-allowed disabled:opacity-50',
                  gw?.route_mode === m
                    ? 'bg-zinc-900 text-white border-zinc-900'
                    : 'text-zinc-600 bg-white border-zinc-200 hover:bg-zinc-50',
                )}
              >
                {routeBusy && routeTarget === m ? (
                  <Loader2 size={12} className="mr-1 inline animate-spin" />
                ) : null}
                {m === 'smart' ? 'smart（默认）' : m}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* 实例池性能模式参数与网关超时切换已收进右上角「设置」弹窗 */}

      {/* 池成员列表 */}
      <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm overflow-hidden">
        <div className="px-4 py-3 border-b border-zinc-100 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Network size={15} className="text-teal-600" />
            <span className="text-[14px] font-semibold text-zinc-900">池成员</span>
            <span className="text-[12px] text-zinc-400">已入池的实例会聚合到统一网关地址，未入池实例保持独享</span>
            {visibleSelected.size > 0 && (
              <button
                onClick={() => setPoolSelected(new Set())}
                className="text-[11px] px-2 py-0.5 rounded-md bg-zinc-100 text-zinc-600 hover:bg-zinc-200"
              >
                已选 {visibleSelected.size} · 清除
              </button>
            )}
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => void doAll('start')}
              disabled={visibleSelected.size === 0 || !!allBusy}
              title={visibleSelected.size === 0 ? '请先勾选池成员' : `批量启动 ${visibleSelected.size} 个`}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-green-600 hover:bg-green-700 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {allBusy === 'start' ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
              批量启动{visibleSelected.size > 0 ? `（${visibleSelected.size}）` : ''}
            </button>
            <button
              onClick={() => void doAll('stop')}
              disabled={visibleSelected.size === 0 || !!allBusy}
              title={visibleSelected.size === 0 ? '请先勾选池成员' : `批量停止 ${visibleSelected.size} 个`}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {allBusy === 'stop' ? <Loader2 size={14} className="animate-spin" /> : <Square size={14} />}
              批量停止{visibleSelected.size > 0 ? `（${visibleSelected.size}）` : ''}
            </button>
            <button
              onClick={() => void doAll('test')}
              disabled={visibleSelected.size === 0 || !!allBusy}
              title={visibleSelected.size === 0 ? '请先勾选池成员' : `批量测试 ${visibleSelected.size} 个`}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 border border-teal-100 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {allBusy === 'test' ? <Loader2 size={14} className="animate-spin" /> : <TestTube2 size={14} />}
              批量测试{visibleSelected.size > 0 ? `（${visibleSelected.size}）` : ''}
            </button>
            <button
              onClick={() => setReleaseMode('all')}
              disabled={visibleSelected.size === 0 || !!releaseAllBusy}
              title={visibleSelected.size === 0 ? '请先勾选池成员' : `批量释放 ${visibleSelected.size} 个`}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-red-600 bg-red-50 border border-red-100 hover:bg-red-100 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {releaseAllBusy ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
              批量释放{visibleSelected.size > 0 ? `（${visibleSelected.size}）` : ''}
            </button>
            <select
              value={filter}
              onChange={(e) => setFilter(e.target.value as typeof filter)}
              className="px-2.5 py-1.5 rounded-lg border border-zinc-200 bg-white text-[12px] text-zinc-600 outline-none"
            >
              <option value="all">全部实例</option>
              <option value="running">运行中</option>
              <option value="stopped">已停止</option>
            </select>
            <div
              className={clsx(
                'relative flex items-center rounded-lg border border-zinc-200 bg-white transition-all duration-200 overflow-hidden',
                searchFocus || search ? 'w-52' : 'w-9',
              )}
            >
              <Search size={14} className="absolute left-2.5 text-zinc-400 pointer-events-none" />
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                onFocus={() => setSearchFocus(true)}
                onBlur={() => setSearchFocus(false)}
                placeholder="搜索池成员"
                className={clsx(
                  'w-full bg-transparent py-1.5 pl-8 pr-2 text-[12px] outline-none placeholder:text-zinc-300 transition-opacity',
                  searchFocus || search ? 'opacity-100' : 'opacity-0',
                )}
              />
            </div>
          </div>
        </div>
{members.length > 0 ? (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-zinc-400 border-b border-zinc-100">
                <th className="py-3 pl-4 w-8">
                  <input
                    type="checkbox"
                    checked={allChecked}
                    onChange={() => setPoolSelected(allChecked ? new Set() : new Set(members.map((i) => i.name)))}
                    className="accent-zinc-900"
                    title="全选/取消全选"
                  />
                </th>
                <th className="py-3 pl-2">名称 / 节点 IP</th>
                <th className="py-3 pl-2">端口</th>
                <th className="py-3 pl-2">健康状态</th>
                <th className="py-3 pl-2">链路质量</th>
                <th className="py-3 pl-2 pr-4 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {members.map((i) => {
                const [cls, label] = statusBadge(i.status)
                return (
                  <tr key={i.name} className="border-b border-zinc-50 hover:bg-zinc-50/50">
                    <td className="py-2.5 pl-4 w-8">
                      <input
                        type="checkbox"
                        checked={poolSelected.has(i.name)}
                        onChange={() => toggleSelected(i.name)}
                        className="accent-zinc-900"
                      />
                    </td>
                    <td className="py-2.5 pl-2">
                      <div className="font-medium text-zinc-800">{i.node}</div>
                      <div className="text-[11px] text-zinc-400">
                        {i.ip ? (
                          <button
                            onClick={() => void copyText(i.ip, '节点 IP')}
                            className="flex items-center gap-1 text-zinc-400 hover:text-zinc-600 hover:underline"
                            title="点击复制"
                          >
                            <code className="text-[12px]">{i.ip}</code>
                            <Copy size={10} />
                          </button>
                        ) : (
                          '—'
                        )}
                      </div>
                    </td>
                    <td className="py-2.5 pl-2 text-zinc-500">{i.port}</td>
                    <td className="py-2.5 pl-2">
                      <div className="flex flex-col items-start gap-1">
                        <span className={clsx('inline-block px-2 py-0.5 rounded-full text-xs font-medium', cls)}>{label}</span>
                        {testBadge(testResults[i.name])}
                      </div>
                    </td>
                    <td className="py-2.5 pl-2">{qualityBadge(qualityByName.get(i.name))}</td>
<td className="py-2.5 pl-2 pr-4">
                      <div className="flex items-center justify-end gap-1.5">
                        {i.status === 'Running' ? (
                          <button
                            onClick={() => void doRowOp(i.name, 'stop')}
                            disabled={!!rowBusy[i.name]}
                            className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-zinc-700 bg-zinc-100 hover:bg-zinc-200 disabled:cursor-not-allowed disabled:opacity-60"
                          >
                            {rowBusy[i.name] === 'stop' ? <Loader2 size={12} className="animate-spin" /> : null}
                            {rowBusy[i.name] === 'stop' ? '停止中…' : '停止'}
                          </button>
                        ) : (
                          <button
                            onClick={() => void doRowOp(i.name, 'start')}
                            disabled={!!rowBusy[i.name]}
                            className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-white bg-green-600 hover:bg-green-700 disabled:cursor-not-allowed disabled:opacity-60"
                          >
                            {rowBusy[i.name] === 'start' ? <Loader2 size={12} className="animate-spin" /> : null}
                            {rowBusy[i.name] === 'start' ? '启动中…' : '启动'}
                          </button>
                        )}
                        <button
                          onClick={() => void doRowOp(i.name, 'test')}
                          disabled={!!rowBusy[i.name]}
                          className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-60"
                        >
                          {rowBusy[i.name] === 'test' ? <Loader2 size={12} className="animate-spin" /> : <TestTube2 size={12} />}
                          {rowBusy[i.name] === 'test' ? '测试中…' : '测试'}
                        </button>
<button
                          onClick={() => void doRelease(i.name)}
                          disabled={kickBusy === i.name || !!rowBusy[i.name]}
                          className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-red-600 bg-red-50 hover:bg-red-100 disabled:cursor-not-allowed disabled:opacity-60"
                        >
                          {kickBusy === i.name ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
                          {kickBusy === i.name ? '释放中…' : '释放'}
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        ) : poolMembers.length > 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-zinc-400">
<p className="text-[13px] mb-1">没有匹配当前搜索或筛选的池成员</p>
<p className="text-[12px]">试试调整搜索或筛选条件</p>
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center py-16 text-zinc-400">
<p className="text-[13px] mb-1">暂无池成员</p>
<p className="text-[12px]">在「节点池」页勾选节点，以「进池」方式批量添加（聚合到统一网关）</p>
          </div>
        )}
      </div>

      {/* S3: 释放确认弹窗（完全 / 仅运行中） */}
      {releaseMode && (
        <div
          className="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4"
          onClick={() => setReleaseMode(null)}
        >
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-md p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <h3 className="text-lg font-semibold text-zinc-900">释放实例</h3>
            <p className="text-[13px] text-zinc-600">
              已勾选 <b className="text-zinc-900">{selTotal}</b> 个池成员
              ，其中运行中 <b className="text-zinc-900">{selRunning}</b> 个。选择释放范围（将关闭并删除实例定义）：
            </p>
            <div className="grid grid-cols-2 gap-3">
              <button
                onClick={() => void doReleaseAll('running')}
                disabled={selRunning === 0}
                className="flex items-center justify-center gap-1.5 px-4 py-2 rounded-lg text-[13px] text-amber-700 bg-amber-50 border border-amber-100 hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-40"
              >
                <Power size={14} /> 仅释放运行中（{selRunning}）
              </button>
              <button
                onClick={() => void doReleaseAll('all')}
                className="flex items-center justify-center gap-1.5 px-4 py-2 rounded-lg text-[13px] text-red-600 bg-red-50 border border-red-100 hover:bg-red-100"
              >
                <Trash2 size={14} /> 完全释放（{selTotal}）
              </button>
            </div>
            <div className="flex justify-end">
              <button onClick={() => setReleaseMode(null)} className="px-4 py-2 rounded-lg text-[13px] text-zinc-600 hover:bg-zinc-100">
                取消
              </button>
            </div>
          </div>
        </div>
      )}

      {/* 页面设置弹窗（右上角齿轮）：性能模式参数 + 网关超时切换 */}
      {settingsOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-[722px] max-h-[86vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold text-zinc-900">实例池设置</h2>
              <button onClick={() => setSettingsOpen(false)} className="p-1.5 rounded-lg hover:bg-zinc-100">
                <X size={18} />
              </button>
            </div>

            {/* 性能模式参数（折叠面板，默认收起） */}
            <section className="border border-zinc-200 rounded-xl overflow-hidden">
              <button
                type="button"
                onClick={() => setOpenSections((s) => ({ ...s, perf: !s.perf }))}
                className={clsx('w-full flex items-center justify-between px-4 py-3 text-left hover:bg-zinc-50', openSections.perf && 'border-b border-zinc-200')}
              >
                <span className="text-[14px] font-semibold text-zinc-900">实例池性能模式 · 参数</span>
                <ChevronDown size={16} className={clsx('text-zinc-400 transition-transform', !openSections.perf && '-rotate-90')} />
              </button>
              {openSections.perf && (
                <div className="p-4 space-y-4">
                  <p className="text-[12px] text-zinc-400">
                    链路级主动探活（经实例出口发真实请求）+ 质量加权路由：坏节点自动降权/剔除，熔断到期自动回归。总开关在路由模式处。
                  </p>
              <div className="space-y-2.5 pt-1">
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">探活间隔（秒，默认 45）</label>
                  <input
                    type="number"
                    min={0}
                    value={poolForm.pool_probe_interval_sec}
                    onChange={(e) => setPoolForm({ ...poolForm, pool_probe_interval_sec: Number(e.target.value) })}
                    className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="text-[11px] text-zinc-400 w-8 shrink-0 text-right">0=关</span>
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">质量窗口（分钟，默认 10）</label>
                  <input
                    type="number"
                    min={1}
                    value={poolForm.pool_quality_window_min}
                    onChange={(e) => setPoolForm({ ...poolForm, pool_quality_window_min: Number(e.target.value) })}
                    className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="w-8 shrink-0" />
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">熔断阈值（连续失败，默认 3）</label>
                  <input
                    type="number"
                    min={1}
                    value={poolForm.pool_breaker_threshold}
                    onChange={(e) => setPoolForm({ ...poolForm, pool_breaker_threshold: Number(e.target.value) })}
                    className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="w-8 shrink-0" />
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">半开间隔（秒，默认 60）</label>
                  <input
                    type="number"
                    min={1}
                    value={poolForm.pool_halfopen_interval_sec}
                    onChange={(e) => setPoolForm({ ...poolForm, pool_halfopen_interval_sec: Number(e.target.value) })}
                    className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="w-8 shrink-0" />
                </div>
              </div>

              <div className="pt-2 border-t border-zinc-100">
                <div className="text-[13px] font-medium text-zinc-700 mb-2">并发设置</div>
                <div className="space-y-2.5">
                  {([
                    ['pool_race_copies', '竞速并行', 1, 4],
                    ['scan_concurrency', '节点扫描', 1, 16],
                    ['batch_concurrency', '批量启停/释放', 1, 16],
                    ['test_concurrency', '一键测试', 1, 16],
                    ['pool_probe_concurrency', '链路探活', 1, 16],
                  ] as const).map(([key, label, lo, hi]) => (
                    <div key={key} className="flex items-center gap-3">
                      <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">{label}（{lo}~{hi}）</label>
                      <input
                        type="number"
                        min={lo}
                        max={hi}
                        value={poolForm[key]}
                        onChange={(e) => setPoolForm({ ...poolForm, [key]: Number(e.target.value) })}
                        className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                      />
                      <span className="w-8 shrink-0" />
                    </div>
                  ))}
                </div>
                <p className="text-[11px] text-zinc-400 mt-1.5">并发过高可能引起进程风暴，建议保持默认</p>
              </div>

              <div className="flex items-center justify-between">
                <div className="flex items-center space-x-3">
                  <label className="relative inline-flex items-center cursor-pointer">
                    <input
                      type="checkbox"
                      checked={poolProbeEnabled}
                      onChange={(e) => setPoolProbeEnabled(e.target.checked)}
                      className="sr-only peer"
                    />
                    <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
                  </label>
                  <span className="text-sm text-zinc-700">链路主动探活</span>
                </div>
                <button
                  onClick={() => void handleSavePool()}
                  disabled={savingPool}
                  className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-60"
                >
                  {savingPool ? <Loader2 size={14} className="animate-spin" /> : null}
                  {savingPool ? '保存中…' : '保存'}
                </button>
              </div>
                </div>
              )}
            </section>

            {/* 网关超时切换（折叠面板，默认收起） */}
            <section className="border border-zinc-200 rounded-xl overflow-hidden">
              <button
                type="button"
                onClick={() => setOpenSections((s) => ({ ...s, timeout: !s.timeout }))}
                className={clsx('w-full flex items-center justify-between px-4 py-3 text-left hover:bg-zinc-50', openSections.timeout && 'border-b border-zinc-200')}
              >
                <span className="text-[14px] font-semibold text-zinc-900">网关超时切换</span>
                <ChevronDown size={16} className={clsx('text-zinc-400 transition-transform', !openSections.timeout && '-rotate-90')} />
              </button>
              {openSections.timeout && (
                <div className="p-4 space-y-4">
                  <p className="text-[12px] text-zinc-400">
                    每次请求在区间内随机取超时值，避免固定超时被上游识别为定时扫描；最小值防止过密重试
                  </p>
              <div className="space-y-2.5 pt-1">
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">首字超时 TTFT（毫秒，默认 10s）</label>
                  <input
                    type="number"
                    min={1}
                    value={timeoutForm.timeout_ttft_min_ms}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_ttft_min_ms: Number(e.target.value) })}
                    className="w-24 shrink-0 px-2 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="text-zinc-400">~</span>
                  <input
                    type="number"
                    min={1}
                    value={timeoutForm.timeout_ttft_max_ms}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_ttft_max_ms: Number(e.target.value) })}
                    className="w-24 shrink-0 px-2 py-2 border rounded-lg text-[13px] text-right"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">块间静默超时（毫秒，默认 5s）</label>
                  <input
                    type="number"
                    min={1}
                    value={timeoutForm.timeout_silence_min_ms}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_silence_min_ms: Number(e.target.value) })}
                    className="w-24 shrink-0 px-2 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="text-zinc-400">~</span>
                  <input
                    type="number"
                    min={1}
                    value={timeoutForm.timeout_silence_max_ms}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_silence_max_ms: Number(e.target.value) })}
                    className="w-24 shrink-0 px-2 py-2 border rounded-lg text-[13px] text-right"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">切换前并行探测数（默认 2~3）</label>
                  <input
                    type="number"
                    min={1}
                    value={timeoutForm.failover_probe_min}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, failover_probe_min: Number(e.target.value) })}
                    className="w-24 shrink-0 px-2 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="text-zinc-400">~</span>
                  <input
                    type="number"
                    min={1}
                    value={timeoutForm.failover_probe_max}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, failover_probe_max: Number(e.target.value) })}
                    className="w-24 shrink-0 px-2 py-2 border rounded-lg text-[13px] text-right"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">调用日志保留上限（默认 5000）</label>
                  <input
                    type="number"
                    min={100}
                    value={timeoutForm.call_log_max}
                    onChange={(e) => setTimeoutForm({ ...timeoutForm, call_log_max: Number(e.target.value) })}
                    className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                  />
                  <span className="w-5 shrink-0" />
                  <span className="w-5 shrink-0" />
                </div>
              </div>

              <div className="flex items-center justify-between">
                <div className="flex items-center space-x-3">
                  <label className="relative inline-flex items-center cursor-pointer">
                    <input
                      type="checkbox"
                      checked={showNodePrefix}
                      onChange={(e) => handleShowNodePrefixChange(e.target.checked)}
                      disabled={prefixBusy}
                      className="sr-only peer"
                    />
                    <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
                  </label>
                  <span className="text-sm text-zinc-700">对话流首段展示「节点 · 模型」前缀</span>
                </div>
                <button
                  onClick={() => void handleSaveTimeout()}
                  disabled={savingTimeout}
                  className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-60"
                >
                  {savingTimeout ? <Loader2 size={14} className="animate-spin" /> : null}
                  {savingTimeout ? '保存中…' : '保存超时配置'}
                </button>
              </div>
                </div>
              )}
            </section>

            {/* auto 虚拟模型（折叠面板，默认收起；默认关闭） */}
            <section className="border border-zinc-200 rounded-xl overflow-hidden">
              <button
                type="button"
                onClick={() => setOpenSections((s) => ({ ...s, auto: !s.auto }))}
                className={clsx('w-full flex items-center justify-between px-4 py-3 text-left hover:bg-zinc-50', openSections.auto && 'border-b border-zinc-200')}
              >
                <span className="text-[14px] font-semibold text-zinc-900">auto 模型 · 智能选路</span>
                <span className="flex items-center gap-2">
                  {autoForm.enabled ? (
                    <span className="inline-block px-2 py-0.5 rounded-full text-[11px] font-medium bg-teal-50 text-teal-700">已开启</span>
                  ) : null}
                  <ChevronDown size={16} className={clsx('text-zinc-400 transition-transform', !openSections.auto && '-rotate-90')} />
                </span>
              </button>
              {openSections.auto && (
                <div className="p-4 space-y-4">
                  <p className="text-[12px] text-zinc-400">
                    开启后 /v1/models 顶部出现虚拟模型 auto：客户端只填 auto，网关按「权重 × 实测成功率」自动选模型，失败沿候选链无感降级；上下文装不下的模型自动避开。实例（节点）选择仍由上方路由模式负责。
                  </p>
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center space-x-3">
                      <label className="relative inline-flex items-center cursor-pointer">
                        <input
                          type="checkbox"
                          checked={autoForm.enabled}
                          onChange={(e) => setAutoForm((f) => ({ ...f, enabled: e.target.checked }))}
                          className="sr-only peer"
                        />
                        <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
                      </label>
                      <span className="text-sm text-zinc-700">启用 auto 模型</span>
                    </div>
                    <select
                      value={autoForm.strategy || 'balanced'}
                      onChange={(e) => setAutoForm((f) => ({ ...f, strategy: e.target.value }))}
                      className="px-3 py-2 border rounded-lg text-[13px] text-zinc-700"
                    >
                      <option value="balanced">均衡 · 按权重分流（推荐）</option>
                      <option value="speed">速度优先 · 权重≥5 中选最快</option>
                      <option value="quality">能力优先 · 按权重锁定</option>
                    </select>
                  </div>
                  <div className="space-y-2">
                    <div className="grid grid-cols-[1fr_88px_120px] gap-2 text-[11px] text-zinc-400 px-1">
                      <span>模型</span>
                      <span className="text-right">权重 0~10</span>
                      <span className="text-right">上下文（token）</span>
                    </div>
                    {autoModelRows.length === 0 && (
                      <p className="text-[12px] text-zinc-400 py-2">网关未运行或暂无免费模型——启动后此表自动列出；也可保存后稍后回来配置。</p>
                    )}
                    {autoModelRows.map((m) => (
                      <div key={m} className="grid grid-cols-[1fr_88px_120px] gap-2 items-center">
                        <span className="text-[13px] text-zinc-700 truncate" title={m}>{m}</span>
                        <input
                          type="number"
                          min={0}
                          max={10}
                          value={autoForm.weights?.[m] ?? 5}
                          onChange={(e) =>
                            setAutoForm((f) => ({
                              ...f,
                              weights: { ...(f.weights || {}), [m]: Number(e.target.value) },
                            }))
                          }
                          className="w-full px-2 py-1.5 border rounded-lg text-[13px] text-right"
                        />
                        <input
                          type="number"
                          min={0}
                          placeholder="默认 128k"
                          value={autoForm.context_windows?.[m] ?? ''}
                          onChange={(e) =>
                            setAutoForm((f) => {
                              const ctx = { ...(f.context_windows || {}) }
                              const v = Math.round(Number(e.target.value))
                              if (Number.isFinite(v) && v > 0) ctx[m] = v
                              else delete ctx[m]
                              return { ...f, context_windows: ctx }
                            })
                          }
                          className="w-full px-2 py-1.5 border rounded-lg text-[13px] text-right"
                        />
                      </div>
                    ))}
                  </div>
                  <p className="text-[11px] text-zinc-400">
                    权重 0 = 永不参与；未配置按 5。上下文填真实值（如 1000000 / 200000），留空按保守 128k 处理——超限对话自动避开小上下文模型，填错真实值会被系统的失败学习自动修正。
                  </p>
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] text-zinc-400">保存后即时生效（子进程热重载），无需重启实例/网关</span>
                    <button
                      onClick={() => void handleSaveAuto()}
                      disabled={savingAuto}
                      className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      {savingAuto ? <Loader2 size={14} className="animate-spin" /> : null}
                      {savingAuto ? '保存中…' : '保存'}
                    </button>
                  </div>
                </div>
              )}
            </section>

            {/* 界面刷新（折叠面板，默认收起） */}
            <section className="border border-zinc-200 rounded-xl overflow-hidden">
              <button
                type="button"
                onClick={() => setOpenSections((s) => ({ ...s, ui: !s.ui }))}
                className={clsx('w-full flex items-center justify-between px-4 py-3 text-left hover:bg-zinc-50', openSections.ui && 'border-b border-zinc-200')}
              >
                <span className="text-[14px] font-semibold text-zinc-900">界面刷新</span>
                <ChevronDown size={16} className={clsx('text-zinc-400 transition-transform', !openSections.ui && '-rotate-90')} />
              </button>
              {openSections.ui && (
                <div className="p-4 space-y-4">
                  <p className="text-[12px] text-zinc-400">
                    本页按此间隔自动刷新实例池成员状态与链路质量（轻量轮询）；深度状态校正仍由「刷新」按钮执行
                  </p>
                  <div className="flex items-center gap-3">
                    <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">刷新间隔（秒，默认 5）</label>
                    <input
                      type="number"
                      min={0}
                      max={60}
                      value={uiForm.ui_poll_interval_sec}
                      onChange={(e) => setUiForm({ ui_poll_interval_sec: Number(e.target.value) })}
                      className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                    />
                    <span className="text-[11px] text-zinc-400 w-8 shrink-0 text-right">0=关</span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] text-zinc-400">保存后即时生效</span>
                    <button
                      onClick={() => void handleSaveUi()}
                      disabled={savingUi}
                      className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      {savingUi ? <Loader2 size={14} className="animate-spin" /> : null}
                      {savingUi ? '保存中…' : '保存'}
                    </button>
                  </div>
                </div>
              )}
            </section>
          </div>
        </div>
      )}

      {keyOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-md p-5 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2">
              <KeyRound size={16} className="text-teal-600" />
              <h3 className="text-lg font-semibold text-zinc-900">统一网关密钥</h3>
              <span className="flex-1" />
              <button onClick={() => setKeyOpen(false)} className="text-zinc-400 hover:text-zinc-600">
                <X size={18} />
              </button>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">自定义密钥</label>
              <input
                type="text"
                placeholder="至少 8 个字符；留空 + 保存 = 重置默认"
                value={keyValue}
                onChange={(e) => setKeyValue(e.target.value)}
                className="w-full px-3 py-2 border rounded-lg"
              />
              <p className="text-zinc-500 text-xs">设置后需重新配置已连接的客户端；运行中的网关将自动重启生效</p>
            </div>

            <div className="flex gap-2">
              <button
                onClick={() => void doSaveKey()}
                disabled={keyBusy}
                className="flex items-center justify-center gap-1.5 flex-1 px-4 py-2 rounded-lg text-[13px] text-white bg-teal-600 hover:bg-teal-700 disabled:cursor-not-allowed disabled:opacity-60"
              >
                {keyBusy ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                {keyBusy ? '保存中…' : '保存'}
              </button>
              <button
                onClick={() => setKeyOpen(false)}
                className="px-4 py-2 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
              >
                取消
              </button>
            </div>
          </div>
        </div>
      )}

      {portOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
          onClick={() => { if (!portBusy) setPortOpen(false) }}
        >
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-md p-5 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center gap-2">
              <Network size={16} className="text-teal-600" />
              <h3 className="text-lg font-semibold text-zinc-900">统一网关端口</h3>
              <span className="flex-1" />
              <button onClick={() => setPortOpen(false)} className="text-zinc-400 hover:text-zinc-600">
                <X size={18} />
              </button>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">自定义端口</label>
              <input
                type="text"
                inputMode="numeric"
                placeholder="1-65535；留空 + 保存 = 恢复默认端口"
                value={portValue}
                onChange={(e) => setPortValue(e.target.value)}
                className="w-full px-3 py-2 border rounded-lg"
              />
              <p className="text-zinc-500 text-xs">保存后立即生效（网关会自动重启）；运行中的客户端将暂时无法连接</p>
            </div>

            <div className="flex gap-2">
              <button
                onClick={() => void doSavePort()}
                disabled={portBusy}
                className="flex items-center justify-center gap-1.5 flex-1 px-4 py-2 rounded-lg text-[13px] text-white bg-teal-600 hover:bg-teal-700 disabled:cursor-not-allowed disabled:opacity-60"
              >
                {portBusy ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                {portBusy ? '保存中…' : '保存'}
              </button>
              <button
                onClick={() => setPortOpen(false)}
                className="px-4 py-2 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
              >
                取消
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
})

/** 测试结果徽章：✓ 通过+延迟+详情 / ✗ 失败+原因（无结果返回 null 不占位） */
/** 链路质量徽标（P1 探活评分）：质量分 + 等级 + 平均延迟（无记录显示"未探测"） */
function qualityBadge(r?: PoolQualityRecord) {
  const levelMap: Record<PoolQualityLevel, [string, string]> = {
    healthy: ['bg-green-50 text-green-700', '健康'],
    degraded: ['bg-amber-50 text-amber-700', '较慢'],
    flaky: ['bg-orange-50 text-orange-600', '抖动'],
    down: ['bg-red-50 text-red-600', '不可用'],
  }
  if (!r) {
    return (
      <span className="inline-block px-2 py-0.5 rounded-full text-[11px] font-medium bg-zinc-100 text-zinc-400 whitespace-nowrap">
        未探测
      </span>
    )
  }
  const [cls, label] = levelMap[r.level] ?? levelMap.healthy
  return (
    <span
      className={clsx('inline-block px-2 py-0.5 rounded-full text-[11px] font-medium whitespace-nowrap', cls)}
      title={`成功率 ${(r.success_rate * 100).toFixed(0)}% · 平均延迟 ${r.avg_latency_ms}ms · 连续失败 ${r.consecutive_failures} 次`}
    >
      {r.score} 分 · {label}
      {r.avg_latency_ms > 0 ? ` · ${r.avg_latency_ms}ms` : ''}
    </span>
  )
}

/** 测试结果徽章：✓ 通过+延迟+详情 / ✗ 失败+原因（无结果返回 null 不占位） */
function testBadge(r?: TestResult) {
  if (!r) return null
  if (r.ok) {
    return (
      <span
        className="inline-block max-w-[240px] px-2 py-0.5 rounded-full text-[11px] font-medium bg-green-50 text-green-700 truncate"
        title={r.message}
      >
        ✓ 通过 {r.latency_ms}ms{r.message ? ` · ${r.message}` : ''}
      </span>
    )
  }
  return (
    <span
      className="inline-block max-w-[240px] px-2 py-0.5 rounded-full text-[11px] font-medium bg-red-50 text-red-600 truncate"
      title={r.message || '测试失败'}
    >
      ✗ {r.message || '失败'}
    </span>
  )
}