import { memo, useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { RefreshCw, Play, Square, Trash2, TestTube2, Copy, Loader2, Search, Server, Activity, Settings2, ChevronDown, X } from 'lucide-react'
import { api, type Instance, type TestResult, type PoolQualityRecord, type PoolQualityLevel } from '../lib/api'

/** 链路质量徽标（与实例池页一致）：质量分 + 等级 + 延迟（无记录显示"未探测"）；
 *  悬浮展示窗口明细（2026-08-24 问题2，与 PoolPage.qualityBadge 对齐） */
function qualityBadge(r?: PoolQualityRecord) {
  const levelMap: Record<PoolQualityLevel, [string, string]> = {
    healthy: ['bg-green-50 text-green-700', '健康'],
    degraded: ['bg-amber-50 text-amber-700', '较慢'],
    flaky: ['bg-orange-50 text-orange-600', '抖动'],
    down: ['bg-red-50 text-red-600', '不可用'],
    unknown: ['bg-zinc-100 text-zinc-400', '探测中'],
  }
  if (!r) {
    return (
      <span className="inline-block px-2 py-0.5 rounded-full text-[11px] font-medium bg-zinc-100 text-zinc-400 whitespace-nowrap">
        未探测
      </span>
    )
  }
  const [cls, label] = levelMap[r.level] ?? levelMap.unknown
  const sampleCount = r.samples?.length ?? 0
  const title =
    `成功率 ${(r.success_rate * 100).toFixed(0)}% · 平均延迟 ${r.avg_latency_ms}ms · ` +
    `窗口样本 ${sampleCount} 个 · 连续失败 ${r.consecutive_failures} 次` +
    (r.last_probe_ts > 0 ? ` · 最近探测 ${new Date(r.last_probe_ts * 1000).toLocaleTimeString()}` : '') +
    (r.last_error ? ` · 最近失败: ${r.last_error}` : '')
  return (
    <span
      className={clsx('inline-block px-2 py-0.5 rounded-full text-[11px] font-medium whitespace-nowrap', cls)}
      title={title}
    >
      {r.score} 分 · {label}
      {r.avg_latency_ms > 0 ? ` · ${r.avg_latency_ms}ms` : ''}
    </span>
  )
}

function statusBadge(st: Instance['status']): [string, string] {
  if (st === 'Running') return ['bg-green-50 text-green-700', '运行中']
  if (st === 'Stopped') return ['bg-zinc-100 text-zinc-500', '已停止']
  if (st === 'Starting' || st === 'Stopping') return ['bg-amber-50 text-amber-700', st === 'Starting' ? '启动中' : '停止中']
  if (st && typeof st === 'object' && 'Error' in st) return ['bg-red-50 text-red-600', `错误:${(st as { Error: string }).Error}`]
  return ['bg-zinc-100 text-zinc-500', '未知']
}

// M10: memo 包裹——props（toast 稳定化后）引用不变，App 因任务面板 tasks 变化的重渲染不波及本页大表格
export default memo(function InstancesPage({
  toast,
}: {
  toast: (msg: string, ok?: boolean) => void
}) {
  const [instances, setInstances] = useState<Instance[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  // 测试结果（行内徽章正反馈）：name → TestResult
  const [testResults, setTestResults] = useState<Record<string, TestResult>>({})
  // 链路质量（V4）：按实例名匹配；探活开关状态
  const [quality, setQuality] = useState<PoolQualityRecord[] | null>(null)
  const [probeSolo, setProbeSolo] = useState(true)
  const [qualityBusy, setQualityBusy] = useState(false)
  const [search, setSearch] = useState('')
  const [searchFocus, setSearchFocus] = useState(false)
  const [filter, setFilter] = useState<'all' | 'running' | 'stopped'>('all')
  const [refreshing, setRefreshing] = useState(false)
  // 手动刷新进度：{ done: 已检查数量, total: 实例总数 }，null = 不在刷新
  const [refreshProgress, setRefreshProgress] = useState<{ done: number; total: number } | null>(null)
  // 界面刷新（U3）：轮询间隔（秒，0 = 关闭轮询）（生效值 + 表单值）
  const [uiPollSec, setUiPollSec] = useState(5)
  const [uiForm, setUiForm] = useState({ ui_poll_interval_sec: 5 })
  // 页面设置弹窗（折叠面板：每个配置分组一个 section，默认收起）
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [openSections, setOpenSections] = useState<{ ui: boolean }>({ ui: false })

  // G31: toast 用 ref 封装（App 的 showToast 每次渲染重建）——load 依赖不含 toast，轮询定时器不因 toast 重启
  const toastRef = useRef(toast)
  toastRef.current = toast

  // M9: 轮询代次守卫——load 开始记代，响应后比对，过期响应丢弃（慢响应不叠加、旧快照不覆盖新状态）
  const loadGen = useRef(0)
  const load = useCallback(async (silent = true) => {
    const gen = ++loadGen.current
    try {
      const [ins, q, c] = await Promise.all([api.listInstances(), api.poolQuality(), api.configGet()])
      if (gen !== loadGen.current) return
      setInstances(ins)
      setQuality(q.records ?? null)
      setProbeSolo(c.probe_solo_enabled)
    } catch (e) {
      if (gen !== loadGen.current) return
      if (!silent) toastRef.current(String(e), false)
    }
  }, [])

  // 独享实例探测开关（V4）：控制是否对独享实例做链路质量检查
  const doToggleProbeSolo = async () => {
    const next = !probeSolo
    setProbeSoloBusy(true)
    try {
      await api.configSet('probe_solo_enabled', String(next))
      setProbeSolo(next)
      toast(next ? '已开启独享实例链路探测' : '已关闭独享实例链路探测（只探测池成员）', true)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setProbeSoloBusy(false)
    }
  }

  // 立即探测（V4）：手动触发一轮链路探活
  const doProbeNow = async () => {
    setQualityBusy(true)
    try {
      const q = await api.poolQualityProbe()
      setQuality(q.records ?? null)
      toast(`链路探活完成：healthy ${q.healthy} · degraded ${q.degraded} · flaky ${q.flaky} · down ${q.down}`, q.down === 0)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setQualityBusy(false)
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

  // U3: 挂载时读界面刷新配置（轮询间隔）一次，填表单 + 定生效值
  useEffect(() => {
    api
      .configGet()
      .then((c) => {
        setUiForm({ ui_poll_interval_sec: c.ui_poll_interval_sec })
        setUiPollSec(c.ui_poll_interval_sec)
      })
      .catch(() => {})
  }, [])

  // 自动轮询（U3）：按配置间隔轻量刷新列表与状态（0 = 关闭）；深度校正仍由手动「刷新」按钮承担
  useEffect(() => {
    void load()
    if (uiPollSec <= 0) return
    const timer = setInterval(() => void load(), uiPollSec * 1000)
    return () => clearInterval(timer)
  }, [load, uiPollSec])

  // 手动刷新：按名称分批（每批并发 CHECK_BATCH）调用后端校正状态，
  // 每批返回后更新列表并累计进度，全部完成后按钮文字恢复「刷新」
  const CHECK_BATCH = 5
  const doRefresh = async () => {
    // M9: 手动深度校正开始即作废在途轮询响应（避免校正后的旧快照回退覆盖）
    loadGen.current++
    // 本页只管理独享实例（池成员在实例池页管理），刷新只校正独享
    const names = instances.filter((i) => !i.join_gateway).map((i) => i.name)
    const total = names.length
    if (total === 0) {
      await load(false)
      return
    }
    setRefreshing(true)
    setRefreshProgress({ done: 0, total })
    let done = 0
    try {
      for (let i = 0; i < names.length; i += CHECK_BATCH) {
        const batch = names.slice(i, i + CHECK_BATCH)
        const updated = await api.refreshStates(batch)
        if (updated.length > 0) {
          // 函数式合并，避免并发批次间基于旧 state 互相覆盖
          setInstances((prev) => {
            const map = new Map(prev.map((it) => [it.name, it]))
            for (const u of updated) map.set(u.name, u)
            return [...map.values()]
          })
        }
        done += updated.length
        setRefreshProgress({ done, total })
      }
    } catch (e) {
      toast(String(e), false)
    } finally {
      // M9: 校正完成再作废一次在途轮询响应（刷新期间启动的轮询不覆盖已校正的合并结果）
      loadGen.current++
      setRefreshing(false)
      setRefreshProgress(null)
    }
  }

  const toggle = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  // 独享实例 = 未入池（页面边界：本页只显示独享，池成员在实例池页）
  const soloInstances = instances.filter((i) => !i.join_gateway)

  // 链路质量按实例名索引
  const qualityByName = new Map<string, PoolQualityRecord>()
  if (quality) for (const r of quality) qualityByName.set(r.name, r)

  // 前端过滤：搜索（名称/节点/IP/端口）+ 状态筛选
  const filtered = soloInstances.filter((i) => {
    const q = search.trim().toLowerCase()
    const hit =
      !q ||
      i.name.toLowerCase().includes(q) ||
      i.node.toLowerCase().includes(q) ||
      (i.ip || '').toLowerCase().includes(q) ||
      String(i.port).includes(q)
    if (!hit) return false
    if (filter === 'running' && i.status !== 'Running') return false
    if (filter === 'stopped' && i.status !== 'Stopped') return false
    return true
  })

  const selectedAll = filtered.length > 0 && filtered.every((i) => selected.has(i.name))

  const toggleAll = () => {
    if (selectedAll) setSelected(new Set())
    else setSelected(new Set(filtered.map((i) => i.name)))
  }

  // 忙态：optimistic —— 变化触发重渲染；key=实例名，值为该实例正在进行的操作
  const [pending, setPending] = useState<Record<string, 'start' | 'stop'>>({})
  // P2 audit: 批量操作忙态按动作区分（对齐 PoolPage allBusy）+ 行内测试/释放忙态
  const [batchKind, setBatchKind] = useState<'start' | 'stop' | 'delete' | null>(null)
  const [rowTestBusy, setRowTestBusy] = useState<Record<string, boolean>>({})
  const [rowRemoveBusy, setRowRemoveBusy] = useState<Record<string, boolean>>({})
  // P2 audit: 设置弹窗「保存」忙态
  const [savingUi, setSavingUi] = useState(false)
  // P2 audit: 链路探测开关切身忙态（防连点重入）
  const [probeSoloBusy, setProbeSoloBusy] = useState(false)

  // 标记/清除某实例的进行中操作
  const setOp = (name: string, op: 'start' | 'stop' | null) => {
    setPending((prev) => {
      const next = { ...prev }
      if (op) next[name] = op
      else delete next[name]
      return next
    })
  }

  const doStart = async (name: string) => {
    setOp(name, 'start')
    try {
      await api.startInstance(name)
      toast(`已启动实例 ${name}`)
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setOp(name, null)
    }
  }

  const doStop = async (name: string) => {
    setOp(name, 'stop')
    try {
      await api.stopInstance(name)
      toast(`已停止实例 ${name}`)
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setOp(name, null)
    }
  }

const doRemove = async (name: string) => {
    if (!confirm(`确定释放实例 ${name}？将关闭实例并释放节点。`)) return
    setRowRemoveBusy((prev) => ({ ...prev, [name]: true }))
    try {
      await api.removeInstance(name)
      toast(`已释放实例 ${name}`)
      setSelected((prev) => {
        const next = new Set(prev)
        next.delete(name)
        return next
      })
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setRowRemoveBusy((prev) => {
        const next = { ...prev }
        delete next[name]
        return next
      })
    }
  }

  const doTest = async (name: string) => {
    setRowTestBusy((prev) => ({ ...prev, [name]: true }))
    try {
      const r = await api.testInstance(name)
      setTestResults((prev) => ({ ...prev, [name]: r }))
      if (r.ok) toast(`「${name}」测试通过：${r.message}（${r.latency_ms}ms）`)
      // 失败 toast 与表格徽章一致：直接显示 r.message（已含完整文案，避免重复实例名）
      else toast(r.message || '测试失败', false)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setRowTestBusy((prev) => {
        const next = { ...prev }
        delete next[name]
        return next
      })
    }
  }




  const batch = async (kind: 'start' | 'stop' | 'delete') => {
    const names = [...selected]
    if (names.length === 0) {
      toast('请先勾选实例')
      return
    }
if (kind === 'delete' && !confirm(`确定释放选中的 ${names.length} 个实例？将自动关闭并释放节点。`)) return
    setBatchKind(kind)
    try {
      const fn =
        kind === 'start' ? api.batchStart : kind === 'stop' ? api.batchStop : api.batchDelete
      const r = await fn(names)
      const skippedPart = kind === 'start' && (r.skipped_count ?? 0) > 0 ? `，跳过已运行 ${r.skipped_count}` : ''
      toast(
        `${kind === 'start' ? '启动' : kind === 'stop' ? '停止' : '释放'}成功 ${r.success_count} 个` +
          skippedPart +
          (r.error_count ? `，失败 ${r.error_count}` : ''),
        r.error_count === 0,
      )
      if (kind === 'delete') setSelected(new Set())
      await load()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setBatchKind(null)
    }
  }

  // 一键测试：对勾选的实例并行探测连通性，结果逐行回填徽章，汇总提示
  const [testBusy, setTestBusy] = useState(false)
  const doBatchTest = async () => {
    const names = [...selected]
    if (names.length === 0) {
      toast('请先勾选实例')
      return
    }
    setTestBusy(true)
    try {
      // D2：仅测试已启动（Running）实例；未启动计入「跳过」，避免误报失败。
      const statusByName = new Map(instances.map((i) => [i.name, i.status]))
      const running = names.filter((n) => statusByName.get(n) === 'Running')
      const skipped = names.length - running.length
      if (running.length === 0) {
        toast(`勾选的实例均未启动（${names.length} 个），无需测试`, false)
        return
      }
      const results = await Promise.allSettled(running.map((n) => api.testInstance(n)))
      let ok = 0
      let fail = 0
      const updated: Record<string, TestResult> = {}
      running.forEach((n, i) => {
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
      toast(`测试完成：成功 ${ok} 个，失败 ${fail} 个${skipped ? `，跳过未启动 ${skipped} 个` : ''}`, fail === 0)
      await load()
    } finally {
      setTestBusy(false)
    }
  }

  const copyText = async (text: string, label: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast(`已复制${label}`)
    } catch {
      /* ignore */
    }
  }

  return (
    <div className="p-6 space-y-4">
      {/* 工具条：标题 + 数量小字，右侧仅刷新 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Server size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">独享管理</h1>
          <span className="text-[12px] text-zinc-400">{soloInstances.length} 个</span>
        </div>
        <div className="flex items-center gap-2">
          {/* V4: 独享实例链路探测开关 + 立即探测 */}
          <div className="flex items-center gap-2 px-2.5 py-1 rounded-lg border border-zinc-200 bg-white" title={probeSolo ? '开启：对所有独享实例做链路质量检查' : '关闭：只探测池成员（实例池页）'}>
            <span className="text-[12px] text-zinc-600 whitespace-nowrap">链路探测</span>
            <button
              onClick={() => void doToggleProbeSolo()}
              disabled={qualityBusy || probeSoloBusy}
              className={clsx(
                'relative inline-flex items-center h-5 w-9 rounded-full transition-colors disabled:opacity-50',
                probeSolo ? 'bg-teal-600' : 'bg-zinc-300',
              )}
              aria-label="独享实例链路探测开关"
            >
              <span className={clsx('inline-block w-3.5 h-3.5 rounded-full bg-white shadow transition-transform', probeSolo ? 'translate-x-[18px]' : 'translate-x-1')} />
            </button>
          </div>
          <button
            onClick={() => void doProbeNow()}
            disabled={qualityBusy}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 border border-teal-100 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {qualityBusy ? <Loader2 size={14} className="animate-spin" /> : <Activity size={14} />}
            {qualityBusy ? '探测中…' : '立即探测'}
          </button>
          <button
            onClick={() => void doRefresh()}
            disabled={refreshing}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:cursor-not-allowed disabled:opacity-70"
          >
            <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
            {refreshProgress ? `刷新 ${refreshProgress.done} / ${refreshProgress.total}` : '刷新'}
          </button>
          <button
            onClick={() => setSettingsOpen(true)}
            title="页面设置（界面刷新等）"
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
          >
            <Settings2 size={14} /> 设置
          </button>
        </div>
      </div>

      {soloInstances.length > 0 && (
        <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm overflow-hidden">
          <div className="px-4 py-3 border-b border-zinc-100 flex items-center justify-between">
            <div className="flex items-center gap-2">
<Server size={15} className="text-teal-600" />
              <span className="text-[14px] font-semibold text-zinc-900">独享</span>
            </div>

            <div className="flex items-center gap-2">
              <button
                onClick={() => void batch('start')}
                disabled={selected.size === 0 || !!batchKind}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-green-600 hover:bg-green-700 disabled:cursor-not-allowed disabled:opacity-40"
              >
                {batchKind === 'start' ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />} 批量启动
              </button>
              <button
                onClick={() => void batch('stop')}
                disabled={selected.size === 0 || !!batchKind}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:cursor-not-allowed disabled:opacity-40"
              >
                {batchKind === 'stop' ? <Loader2 size={14} className="animate-spin" /> : <Square size={14} />} 批量停止
              </button>
              <button
                onClick={() => void doBatchTest()}
                disabled={selected.size === 0 || testBusy}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 border border-teal-100 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-40"
              >
                {testBusy ? <Loader2 size={14} className="animate-spin" /> : <TestTube2 size={14} />} 一键测试
              </button>
              <button
                onClick={() => void batch('delete')}
                disabled={selected.size === 0 || !!batchKind}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-red-600 bg-red-50 border border-red-100 hover:bg-red-100 disabled:cursor-not-allowed disabled:opacity-40"
              >
                {batchKind === 'delete' ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />} 批量释放
              </button>
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
                  placeholder="搜索名称 / 节点 / IP"
                  className={clsx(
                    'w-full bg-transparent py-1.5 pl-8 pr-2 text-[12px] outline-none placeholder:text-zinc-300 transition-opacity',
                    searchFocus || search ? 'opacity-100' : 'opacity-0',
                  )}
                />
              </div>
              <select
                value={filter}
                onChange={(e) => setFilter(e.target.value as typeof filter)}
                className="px-2.5 py-1.5 rounded-lg border border-zinc-200 bg-white text-[12px] text-zinc-600 outline-none"
              >
                <option value="all">全部实例</option>
                <option value="running">运行中</option>
                <option value="stopped">已停止</option>
              </select>
            </div>
          </div>
          {filtered.length > 0 ? (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-zinc-400 border-b border-zinc-100">
                <th className="py-3 pl-4 w-8">
                  <input type="checkbox" checked={selectedAll} onChange={toggleAll} className="accent-zinc-900" />
                </th>
                <th className="py-3 pl-2">名称 / 节点 IP</th>
                <th className="py-3 pl-2">端口</th>

                <th className="py-3 pl-2">密钥</th>
                <th className="py-3 pl-2">链路质量</th>
                <th className="py-3 pl-2">状态</th>
                <th className="py-3 pl-2 pr-4 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
{filtered.map((i) => {
                const isPending = pending[i.name]
                // 乐观状态：操作中直接显示启动中/停止中，覆盖真实状态徽章
                const displayStatus: Instance['status'] = isPending === 'stop' ? 'Stopping' : isPending === 'start' ? 'Starting' : i.status
                const [cls, label] = statusBadge(displayStatus)
                return (
                  <tr key={i.name} className="border-b border-zinc-50 hover:bg-zinc-50/50">
                    <td className="py-2.5 pl-4">
                      <input type="checkbox" checked={selected.has(i.name)} onChange={() => toggle(i.name)} className="accent-zinc-900" />
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
                      <button
                        onClick={() => void copyText(`http://127.0.0.1:${i.port}/v1\n${i.password || ''}`, 'API 地址与密钥')}
                        className="flex items-center gap-1 text-zinc-600 hover:underline"
                        title="点击复制 API 地址与密钥"
                      >
                        <code className="text-[12px] text-zinc-400">{maskKey(i.password)}</code>
                        <Copy size={11} />
                      </button>
                    </td>
<td className="py-2.5 pl-2">{qualityBadge(qualityByName.get(i.name))}</td>
                    <td className="py-2.5 pl-2">
                      <div className="flex flex-col items-start gap-1">
                        <div className="flex items-center gap-1.5">
                          <span className={clsx('inline-block px-2 py-0.5 rounded-full text-xs font-medium', cls)}>{label}</span>
                        </div>
                        {testBadge(testResults[i.name])}
                      </div>
                    </td>
                    <td className="py-2.5 pl-2 pr-4">
                      <div className="flex items-center justify-end gap-1.5">
{i.status === 'Running' ? (
                          <button
                            onClick={() => void doStop(i.name)}
                            disabled={!!pending[i.name]}
                            className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-zinc-700 bg-zinc-100 hover:bg-zinc-200 disabled:cursor-not-allowed disabled:opacity-60"
                          >
                            {pending[i.name] === 'stop' ? <Loader2 size={12} className="animate-spin" /> : null}
                            {pending[i.name] === 'stop' ? '停止中…' : '停止'}
                          </button>
                        ) : (
                          <button
                            onClick={() => void doStart(i.name)}
                            disabled={!!pending[i.name]}
                            className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-white bg-green-600 hover:bg-green-700 disabled:cursor-not-allowed disabled:opacity-60"
                          >
                            {pending[i.name] === 'start' ? <Loader2 size={12} className="animate-spin" /> : null}
                            {pending[i.name] === 'start' ? '启动中…' : '启动'}
                          </button>
                        )}
                        <button onClick={() => void doTest(i.name)} disabled={!!rowTestBusy[i.name]} className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:cursor-not-allowed disabled:opacity-60">
                          {rowTestBusy[i.name] ? <Loader2 size={12} className="animate-spin" /> : <TestTube2 size={12} />}
                          {rowTestBusy[i.name] ? '测试中…' : '测试'}
                        </button>
                        <button onClick={() => void doRemove(i.name)} disabled={!!rowRemoveBusy[i.name]} className="flex items-center gap-1 px-2.5 py-1 rounded-md text-[12px] text-red-600 bg-red-50 hover:bg-red-100 disabled:cursor-not-allowed disabled:opacity-60">
                          {rowRemoveBusy[i.name] ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
                          {rowRemoveBusy[i.name] ? '释放中…' : '释放'}
                        </button>
                      </div>
                    </td>
                  </tr>
)
              })}
            </tbody>
          </table>
          ) : (
          <div className="flex flex-col items-center justify-center py-16 text-zinc-400">
            <p className="text-[13px]">没有匹配「{search || filter}」的实例，试试调整搜索或筛选条件</p>
          </div>
          )}
        </div>
      )}

      {soloInstances.length === 0 && (
        <div className="flex flex-col items-center justify-center py-24 text-zinc-400">
          <p className="text-base mb-2">暂无独享实例</p>
<p className="text-[13px]">在「节点池」页勾选节点，以「独享」方式批量添加；池成员见「实例池」页</p>
        </div>
      )}

      {/* 页面设置弹窗（右上角齿轮）：折叠面板，每个配置分组一个 section（默认收起） */}
      {settingsOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-md max-h-[86vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold text-zinc-900">独享管理设置</h2>
              <button onClick={() => setSettingsOpen(false)} className="p-1.5 rounded-lg hover:bg-zinc-100">
                <X size={18} />
              </button>
            </div>

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
                    本页按此间隔自动刷新实例状态与链路质量（轻量轮询，静默无提示）；深度状态校正仍由「刷新」按钮执行
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
                    <button onClick={() => void handleSaveUi()} disabled={savingUi} className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-60">
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

    </div>
  )
})

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



function maskKey(k: string) {
  if (!k) return '未设置'
  if (k.length <= 8) return k
  return `${k.slice(0, 3)}…${k.slice(-4)}`
}