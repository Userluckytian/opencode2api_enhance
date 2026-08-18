import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import clsx from 'clsx'
import { Loader2, Network, Radar, RefreshCw, Rss, Settings2, Square, Trash2, User, X, ChevronDown } from 'lucide-react'
import { api, type NodeView, type ProbeResult, type ScanProgress, type SubscriptionSource } from '../lib/api'
import { isDesktop } from '../lib/env'
import ResultModal from '../components/ResultModal'

// N1: 扫描按钮三态状态机（idle 空闲 / scanning 扫描中 / stopping 正在停止中）。
type ScanPhase = 'idle' | 'scanning' | 'stopping'

export default function NodesPage({
  toast,
  onTask,
  onRemove,
}: {
  toast: (msg: string, ok?: boolean) => void
  /** V2: 上报全局任务悬浮窗（scan / stop-scan 进度） */
  onTask: (t: { id: string; type: 'scan' | 'stop-scan'; title: string; done: number; total: number; busy?: boolean; error?: boolean }) => void
  /** V2: 移除全局任务（scan 任务在停止/完成后收尾移除） */
  onRemove: (id: string) => void
}) {
  const [nodes, setNodes] = useState<NodeView[]>([])
  const [scan, setScan] = useState<ScanProgress | null>(null)
  // N1: 扫描按钮三态（idle / scanning / stopping）。phaseRef 供 poll 闭包读取当前相位，
  // 避免 React stale closure；停止后由 poll 等后端确认 stopping→done 才回 idle，
  // 杜绝按钮闪烁与进度条 5/10 残留。
  const [phase, setPhase] = useState<ScanPhase>('idle')
  const phaseRef = useRef<ScanPhase>('idle')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [selected, setSelected] = useState<Set<string>>(new Set())
  // 已添加实例的节点：node → 是否入池（join_gateway），用于徽章区分「实例池/独享」
  const [instanceNodes, setInstanceNodes] = useState<Map<string, boolean>>(new Map())
  const [refreshing, setRefreshing] = useState(false)
  // 结果弹窗：扫描完成（running→done）时打开
  const [showResult, setShowResult] = useState(false)
  // 入池/独享动作进行中（弹窗按钮禁用）
  const [acting, setActing] = useState(false)
  // P2 audit: 删除勾选节点 / 保存扫描配置 忙态（spinner + 禁用，防连点）
  const [deleting, setDeleting] = useState(false)
  const [savingScanConf, setSavingScanConf] = useState(false)
  // 追踪上一次扫描状态：仅在「本次扫描 running → done」时弹出结果弹窗
  const prevScanStatusRef = useRef<string | null>(null)
  // N1: 三态切换（ref 同步：poll 闭包只读 phaseRef，避免 stale closure）。
  const setScanPhase = (p: ScanPhase) => {
    phaseRef.current = p
    setPhase(p)
  }
  // N2: 节点扫描配置（按钮 title 并发数与设置面板表单），生效值来自 configGet
  const [scanConf, setScanConf] = useState({ scan_concurrency: 8, stop_scan_concurrency: 4 })
  // N2: 节点池设置弹窗（节点扫描配置折叠面板，默认收起）
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [openSections, setOpenSections] = useState<{ scan: boolean }>({ scan: false })

  // T3: 订阅源列表管理（多条订阅：新增/删除/立即拉取）
  const [subs, setSubs] = useState<SubscriptionSource[]>([])
  const [subOpen, setSubOpen] = useState(false) // 订阅管理弹窗
  const [subBusy, setSubBusy] = useState(false) // 弹窗内动作忙态
  // P3: 确定按钮导入等待计时（「已等待 N 秒」）
  const [subWaitSec, setSubWaitSec] = useState(0)
  const [importingUrl, setImportingUrl] = useState<string | null>(null) // 正在拉取的订阅
  const [deletingUrl, setDeletingUrl] = useState<string | null>(null) // 正在删除的订阅
  // 新增订阅表单
  const [addOpen, setAddOpen] = useState(false)
  const [addUrl, setAddUrl] = useState('')
  const [addInterval, setAddInterval] = useState(30)
  const [addName, setAddName] = useState('') // 可选：手动指定分组名（固定）

  // 首次加载订阅源列表
  useEffect(() => {
    api
      .subscriptionsList()
      .then((r) => setSubs(r.subscriptions ?? []))
      .catch(() => {})
  }, [])

  // N2: 加载节点扫描配置（按钮 title / 设置面板生效值）
  useEffect(() => {
    api
      .configGet()
      .then((c) =>
        setScanConf({ scan_concurrency: c.scan_concurrency, stop_scan_concurrency: c.stop_scan_concurrency }),
      )
      .catch(() => {})
  }, [])

  const loadSubs = useCallback(async () => {
    try {
      const r = await api.subscriptionsList()
      setSubs(r.subscriptions ?? [])
    } catch {
      /* 静默 */
    }
  }, [])

  // T3/P3: 新增订阅 = 保存源 + 立即导入节点池（含「已等待 N 秒」进度）。
  // P3: 10s 看门狗——超时 toast 提示、按钮恢复可用、弹窗不自动关闭；
  // Q2: 超时不丢弃真实请求——后台完成导入后自动刷新节点池（节点获取到即显示，无需手动刷新）。
  const handleAddSubscription = async () => {
    if (!addUrl.trim()) {
      toast('请填写订阅 URL', false)
      return
    }
    const url = addUrl.trim()
    setSubBusy(true)
    setSubWaitSec(0)
    // 每秒刷新「已等待 N 秒」（实际请求不打断，仅前端计时，超时即止损）
    const ticker = window.setInterval(() => {
      setSubWaitSec((s) => s + 1)
    }, 1000)
    const clearTicker = () => window.clearInterval(ticker)
    // 真实请求引用：看门狗超时后仍要等它完成（后台已拉取，完成后节点池自动更新）
    const req = api.subscriptionsAdd(url, addInterval, undefined, addName.trim() || undefined)
    try {
      // P3: 10s 看门狗——race 后端返回与 10s 延时；谁先到谁定夺；后台请求不打断
      const r = await Promise.race([
        req,
        new Promise<null>((resolve) => setTimeout(resolve, 10_000)),
      ])
      if (r === null) {
        // 超时：后台仍在处理。不丢弃真实请求——完成时自动刷新节点池并提示，
        // 节点获取到即在节点池显示（Q2），弹窗不自动关闭，按钮由 finally 恢复
        toast('拉取较慢，后台仍在处理，完成后节点池自动更新', false)
        req
          .then((rr) => {
            if (rr.error) {
              toast(`订阅已添加，但首次拉取失败：${rr.error}`, false)
            } else {
              toast(`已导入 ${rr.imported} 个节点到节点池`, true)
              setAddOpen(false)
              setAddUrl('')
              setAddName('')
              setAddInterval(30)
            }
          })
          .catch(() => {})
          .finally(() => {
            void loadSubs()
            void loadNodes()
          })
        return
      }
      if (r.error) {
        // 返回 error：源已保存但首次拉取失败，提示稍后重试
        toast(`订阅已添加，但首次拉取失败：${r.error}`, false)
        setAddOpen(false)
        setAddUrl('')
        setAddName('')
        setAddInterval(30)
        await loadSubs()
      } else {
        toast(`已导入 ${r.imported} 个节点到节点池`, true)
        setAddOpen(false)
        setAddUrl('')
        setAddName('')
        setAddInterval(30)
        await loadSubs()
        await loadNodes()
      }
    } catch (e) {
      // 请求本身失败（HTTP 错误，如重复订阅）：保持弹窗，可修正后重试
      toast(String(e), false)
    } finally {
      clearTicker()
      setSubWaitSec(0)
      setSubBusy(false)
    }
  }

  // T3: 立即拉取该订阅（按源目标导入）
  const handleImportSub = async (url: string) => {
    setImportingUrl(url)
    try {
      const r = await api.subscriptionsImport(url)
      toast(`订阅拉取成功：导入 ${r.imported} 个节点（${r.target}）`, true)
      await loadNodes()
      await loadSubs()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setImportingUrl(null)
    }
  }

  // T3/V2: 删除订阅——先查使用中实例数并确认，再整体删除（Q1：后端原子完成
  // 停止→释放实例→同步网关→删源→清节点组，前端不再补刀分步释放）
  const handleDeleteSub = async (url: string) => {
    setDeletingUrl(url)
    try {
      // 1) 查询该订阅使用中实例数
      const cnt = await api.subscriptionsCount(url)
      const usingTotal = cnt.running + cnt.stopped
      const msg =
        usingTotal > 0
          ? `该订阅正在使用 ${usingTotal} 个节点（运行中 ${cnt.running}、已停止 ${cnt.stopped}）。删除订阅将停止并释放这些实例，是否继续？`
          : '该订阅没有已创建的实例，删除订阅源即可，是否继续？'
      if (!confirm(msg)) {
        setDeletingUrl(null)
        return
      }
      // 2) 后端一次完成：停止使用中的实例 → 全部停止后移除释放（实例池/独享）→ 同步网关
      //    → 删除订阅源 → 清理该分组节点（Q1）
      const r = await api.subscriptionsDelete(url)
      if (!r.removed) {
        toast('订阅源未找到，可能已删除', false)
        await loadSubs()
        setDeletingUrl(null)
        return
      }
      const released = r.released ?? r.instances?.length ?? 0
      toast(
        released > 0
          ? `已删除订阅，并停止释放 ${released} 个实例${r.removed_nodes ? `，清理 ${r.removed_nodes} 个节点` : ''}`
          : '已删除订阅',
        true,
      )
      await loadSubs()
      await loadNodes()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setDeletingUrl(null)
    }
  }

  // 加载节点 + 轮询扫描进度
// 加载节点 + 轮询扫描进度（注意：这里是链式 setTimeout，卸载时清理）
  const loadNodes = useCallback(async () => {
    try {
      const [ns, insts] = await Promise.all([api.listNodes(), api.listInstances()])
      setNodes(ns)
      setInstanceNodes(new Map(insts.map((i) => [i.node, i.join_gateway])))
    } catch {
      /* 静默 */
    }
  }, [])

  useEffect(() => {
    loadNodes()
    let alive = true
    const poll = async () => {
      if (!alive) return
      try {
        const p = await api.scanStatus()
        if (!alive) return
        const prev = prevScanStatusRef.current
        prevScanStatusRef.current = p.status
        // N1 三态状态机（逻辑不变）：startScan/stopScan 同步置位；poll 只做收尾确认
        // （stopping→done 回 idle）与页面加载后的状态恢复，不再直接驱动按钮态，杜绝闪烁。
        // P5: 停止清空结果 → 改为 status 置 'stopping' 隐藏进度条（进度条仅 running 渲染，
        // 已扫节点结果/延迟徽章保留）；后端确认停止完成（done/error/idle）时回填 scan，
        // 展示后端保留的最终部分结果，可继续勾选使用。
        // V2: 顺带同步全局任务悬浮窗的 scan / stop-scan 进度。
        if (phaseRef.current === 'stopping') {
          // 已请求停止：等后端确认终止（done/error/idle 等价收尾）才回 idle；期间持续上报停止进度
          // M11: error/idle 与 done 同等收敛——停止被中止或状态回落时不再永久 busy
          if (p.status === 'done' || p.status === 'error' || p.status === 'idle') {
            setScanPhase('idle')
            setScan(p) // P5: 回填后端最终部分结果（已扫节点徽章/延迟保留）
            onTask({ id: 'stop-scan', type: 'stop-scan', title: '停止扫描', done: p.stopped_count ?? 0, total: p.stopping_count ?? 0, busy: false })
          } else {
            onTask({ id: 'stop-scan', type: 'stop-scan', title: '停止扫描', done: p.stopped_count ?? 0, total: p.stopping_count ?? 0, busy: true })
          }
        } else if (phaseRef.current === 'scanning') {
          if (p.status === 'running') {
            setScan(p)
            onTask({ id: 'scan', type: 'scan', title: '扫描节点', done: p.current, total: p.total, busy: true })
          } else if (p.status === 'done') {
            setScan(p) // 结果弹窗依赖 done 快照（ok 数/可用节点列表）
            onTask({ id: 'scan', type: 'scan', title: '扫描节点', done: p.total, total: p.total, busy: false })
            // 扫描刚完成（running → done）：弹出结果弹窗
            if (prev === 'running') setShowResult(true)
            setScanPhase('idle')
          } else if (p.status === 'stopping') {
            // 非本页发起的停止（另一会话/上次请求已到达）：scan 任务先收尾移除，再跟随真实状态
            setScanPhase('stopping')
            // P5: 同样隐藏进度条保留结果（进度条仅 running 渲染）
            setScan((prev) => (prev ? { ...prev, status: 'stopping' } : prev))
            onRemove('scan')
            onTask({ id: 'stop-scan', type: 'stop-scan', title: '停止扫描', done: p.stopped_count ?? 0, total: p.stopping_count ?? 0, busy: true })
          } else {
            // M11: scanning 遇 error/idle——扫描中止或未真正开始：收尾 scan 任务（同 done 分支），回 idle
            setScanPhase('idle')
            onTask({ id: 'scan', type: 'scan', title: '扫描节点', done: p.total ?? 0, total: p.total ?? 0, busy: false })
          }
        } else if (p.status === 'running') {
          // idle：页面加载/刷新后恢复真实后端状态（可能在扫或已停）
          setScan(p)
          setScanPhase('scanning')
          onTask({ id: 'scan', type: 'scan', title: '扫描节点', done: p.current, total: p.total, busy: true })
        } else if (p.status === 'stopping') {
          setScanPhase('stopping')
          onRemove('scan')
          onTask({ id: 'stop-scan', type: 'stop-scan', title: '停止扫描', done: p.stopped_count ?? 0, total: p.stopping_count ?? 0, busy: true })
        } else if (p.status === 'done') {
          // idle + 已完成（扫描期间切页、后端已完成）：收尾任何冻结的悬浮任务
          onRemove('scan')
          // 停止中止（error 标记）的 stop-scan 任务一并收尾
          if (p.error) onRemove('stop-scan')
        }
      } catch {
        /* ignore */
      }
      // 无论成功失败都继续轮询（U1 保活：停止后轮询存活，可再扫）
      if (alive) setTimeout(poll, 800)
    }
    setTimeout(poll, 500)
    return () => {
      alive = false
    }
  }, [loadNodes])

  const resultsMap = useMemo(() => {
    const m = new Map<string, ProbeResult>()
    if (scan?.results) for (const r of scan.results) m.set(r.node, r)
    return m
  }, [scan])

  const groups = useMemo(() => {
    const g = new Map<string, NodeView[]>()
    for (const n of nodes) {
      const k = n.group || '其他'
      if (!g.has(k)) g.set(k, [])
      g.get(k)!.push(n)
    }
    return Array.from(g.entries())
  }, [nodes])

  const doRefresh = async () => {
    setRefreshing(true)
    await loadNodes()
    setRefreshing(false)
  }

  // 只扫描选中的节点
  const startScan = async () => {
    const names = [...selected]
    if (names.length === 0) {
      toast('请先勾选要扫描的节点', false)
      return
    }
    setScanPhase('scanning')
    try {
      const p = await api.scanStart({ nodes: names, timeout: 12 })
      setScan(p)
      onTask({ id: 'scan', type: 'scan', title: '扫描节点', done: p.current, total: p.total, busy: true })
      toast(`开始扫描 ${names.length} 个节点…`)
    } catch (e) {
      setScanPhase('idle')
      toast(String(e), false)
    }
  }

  // N1: 停止扫描——点击即同步进入「正在停止中」（禁用态、不闪烁）；
  // P5: 停止不再清空结果——scan 仅置 status 'stopping'（进度条只在 running 渲染，即隐藏），
  // 已扫节点的结果/延迟徽章保留；停止完成后由 poll 用后端保留的最终部分结果回填。
  // V2: scan 任务同步收尾移除（进度移交 stop-scan）；请求失败时 poll 会按后端真实状态重新上报。
  const stopScan = async () => {
    setScanPhase('stopping')
    setScan((prev) => (prev ? { ...prev, status: 'stopping' } : prev))
    onRemove('scan')
    try {
      const p = await api.scanStop()
      // V2: 上报停止进度（done=已停数, total=停止时探测中数）；后续由 poll 持续更新至完成
      onTask({ id: 'stop-scan', type: 'stop-scan', title: '停止扫描', done: p.stopped_count ?? 0, total: p.stopping_count ?? 0, busy: true })
      toast('已请求停止扫描')
    } catch (e) {
      // 请求失败：退回扫描中，由 poll 按后端真实状态校正
      setScanPhase('scanning')
      onTask({ id: 'stop-scan', type: 'stop-scan', title: '停止扫描', done: 0, total: 0, busy: false, error: true })
      toast(String(e), false)
    }
  }

  // N2: 保存节点扫描配置（scan_concurrency / stop_scan_concurrency，1~16 / 1~8）
  const handleSaveScanConf = async () => {
    const { scan_concurrency, stop_scan_concurrency } = scanConf
    if (scan_concurrency < 1 || scan_concurrency > 16) {
      toast('扫描并发需在 1~16 之间', false)
      return
    }
    if (stop_scan_concurrency < 1 || stop_scan_concurrency > 8) {
      toast('停止并发需在 1~8 之间', false)
      return
    }
    setSavingScanConf(true)
    try {
      await api.configSet('scan_concurrency', String(scan_concurrency))
      await api.configSet('stop_scan_concurrency', String(stop_scan_concurrency))
      toast('节点扫描配置已保存', true)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setSavingScanConf(false)
    }
  }

  // P2 audit: 删除勾选节点（仅订阅缓存节点可删；外部 Clash 节点只读跳过）
  const handleDeleteNodes = async () => {
    if (selected.size === 0 || deleting) return
    if (!window.confirm(`删除勾选的 ${selected.size} 个节点？（仅订阅导入的节点可删，外部 Clash 节点只读）`)) return
    setDeleting(true)
    try {
      const r = await api.deleteNodes([...selected])
      toast(`已删除 ${r.removed} 个订阅节点` + (r.removed < selected.size ? '（其余为外部节点，只读跳过）' : ''), true)
      setSelected(new Set())
      await loadNodes()
    } catch (e) {
      toast(String(e), false)
    } finally {
      setDeleting(false)
    }
  }

  const toggleGroup = (g: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(g)) next.delete(g)
      else next.add(g)
      return next
    })
  }

  // 组内「可选」节点 = 未实例化（已实例化节点的复选框禁用，不参与组全选）
  const selectable = (list: NodeView[]) => list.filter((n) => !instanceNodes.has(n.name))

  // 组头勾选态：按可选节点计算（组内全是已实例化时视为未全选）
  const groupSelected = (list: NodeView[]) => {
    const sel = selectable(list)
    return sel.length > 0 && sel.every((n) => selected.has(n.name))
  }

  const toggleGroupSel = (list: NodeView[]) => {
    setSelected((prev) => {
      const next = new Set(prev)
      const sel = selectable(list)
      const all = sel.length > 0 && sel.every((n) => selected.has(n.name))
      for (const n of sel) {
        if (all) next.delete(n.name)
        else next.add(n.name)
      }
      return next
    })
  }

  const toggleNode = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  const scanBtnDisabled = selected.size === 0

  // 扫描结果中的可用节点（去重：剔除已添加为实例的节点）
  const okNodes = useMemo(() => {
    if (!scan || scan.status !== 'done') return []
    return (scan.results ?? [])
      .filter((r) => r.ok && !instanceNodes.has(r.node))
      .map((r) => r.node)
  }, [scan, instanceNodes])

  // 通用批量添加：tag === 'pool' 时额外标记入池（join_gateway）
  // nodes 参数：默认用扫描结果（弹窗），工具条按钮传勾选节点
  const doCommit = async (tag: 'pool' | 'solo', names?: string[]) => {
    const targets = names ?? okNodes
    if (targets.length === 0) return
    setActing(true)
    try {
      const items = targets.map((node) => ({ node }))
      const r = await api.batchAdd(items, undefined, true)
      if (tag === 'pool' && r.added.length > 0) {
        // 进池：只打 join_gateway 标记（不自动启动，启停由实例池页控制）
        for (const a of r.added) {
          try {
            await api.setJoinGateway(a.name, true)
          } catch {
            /* 单条失败不阻断整体 */
          }
        }
      }
      toast(
        `成功添加 ${r.added_count} 个实例` +
          (tag === 'pool' ? '（已入池）' : '（独享）') +
          (r.error_count ? `，跳过/失败 ${r.error_count}` : ''),
        r.error_count === 0,
      )
      // 入池/独享完成后清空勾选态：已添加的节点复选框会变 disabled，
      // 不清空会导致选中态残留且无法手动取消（disabled 不响应点击）
      setSelected(new Set())
      await loadNodes()
      setShowResult(false)
    } catch (e) {
      toast(String(e), false)
    } finally {
      setActing(false)
    }
  }


  return (
    <>
      <div className="p-6 space-y-4">
      {/* 工具条 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Radar size={18} className="text-teal-700" />
          <h1 className="text-[16px] font-semibold text-zinc-900">节点池</h1>
          <span className="px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-500 text-xs font-medium">
            {nodes.length} 个
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
          {/* T3: 订阅导入 → 打开订阅管理弹窗（列表 + 新增 + 拉取 + 删除） */}
          <button
            onClick={() => setSubOpen(true)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
          >
            <Rss size={14} />
            订阅导入
          </button>
          {/* 删除勾选节点（仅订阅缓存节点可删；外部 Clash 节点只读跳过） */}
          <button
            onClick={() => void handleDeleteNodes()}
            disabled={selected.size === 0 || deleting}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] transition-colors',
              selected.size === 0 || deleting
                ? 'bg-zinc-200 text-zinc-500 cursor-not-allowed'
                : 'bg-white border border-red-200 text-red-600 hover:bg-red-50',
            )}
            title={selected.size === 0 ? '请先勾选节点' : '删除勾选的订阅节点'}
          >
            {deleting ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
            {deleting ? '删除中…' : '删除'}
          </button>
          {phase === 'scanning' ? (
            <button
              onClick={() => void stopScan()}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-red-600 hover:bg-red-700"
              title={`停止扫描（${scanConf.stop_scan_concurrency} 并发）`}
            >
              <Square size={14} />
              停止扫描
            </button>
          ) : phase === 'stopping' ? (
            // N1: 停止请求已发出、后端未完全停止：禁用态，转圈不闪烁
            <button
              disabled
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-red-600/60 cursor-not-allowed"
              title="正在停止中…"
            >
              <Loader2 size={14} className="animate-spin" />
              正在停止中…
            </button>
          ) : (
            <button
              onClick={() => void startScan()}
              disabled={scanBtnDisabled}
              className={clsx(
                'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white transition-colors',
                scanBtnDisabled
                  ? 'bg-zinc-200 text-zinc-500 cursor-not-allowed'
                  : 'bg-green-600 hover:bg-green-700 shadow-sm',
              )}
              title={selected.size === 0 ? '请先勾选节点' : `扫描选中节点（${scanConf.scan_concurrency} 并发）`}
            >
              <Radar size={14} /> 扫描选中节点（{selected.size}）
            </button>
          )}
          {/* 入池 / 独享：勾选节点后可用，直接对勾选节点批量添加 */}
          <button
            onClick={() => void doCommit('pool', [...selected])}
            disabled={selected.size === 0 || acting}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] transition-colors',
              selected.size === 0 || acting
                ? 'bg-zinc-200 text-zinc-500 cursor-not-allowed'
                : 'bg-teal-600 text-white hover:bg-teal-700 shadow-sm',
            )}
            title={selected.size === 0 ? '请先勾选节点' : `添加勾选的 ${selected.size} 个节点到实例池`}
          >
            {acting ? <Loader2 size={14} className="animate-spin" /> : <Network size={14} />} 入池
          </button>
          <button
            onClick={() => void doCommit('solo', [...selected])}
            disabled={selected.size === 0 || acting}
            className={clsx(
              'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] transition-colors',
              selected.size === 0 || acting
                ? 'bg-zinc-200 text-zinc-500 cursor-not-allowed'
                : 'bg-blue-600 text-white hover:bg-blue-700 shadow-sm',
            )}
            title={selected.size === 0 ? '请先勾选节点' : `添加勾选的 ${selected.size} 个节点为独享`}
          >
            {acting ? <Loader2 size={14} className="animate-spin" /> : <User size={14} />} 独享
          </button>
          {/* N2: 节点池设置（节点扫描配置折叠面板） */}
          <button
            onClick={() => setSettingsOpen(true)}
            title="节点扫描配置（扫描并发 / 停止并发）"
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
          >
            <Settings2 size={14} /> 设置
          </button>
        </div>
      </div>

      {/* 扫描进度条：只在扫描中（running）显示；点击停止后立即消失（stopping/done 不再渲染） */}
      {scan && scan.status === 'running' && (
        <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-4 space-y-2">
          <div className="flex items-center justify-between text-[12px] text-zinc-500">
            <span>
              {scan.current_node
                ? `扫描中：${scan.current}/${scan.total} · ${scan.current_node}`
                : `扫描中：${scan.current}/${scan.total}`}
            </span>
            <span>{scan.total ? `${Math.round((scan.current / scan.total) * 100)}%` : ''}</span>
          </div>
          <div className="h-2 bg-zinc-100 rounded-full overflow-hidden">
            <div
              className="h-full bg-zinc-900 rounded-full transition-all"
              style={{ width: scan.total ? `${Math.min((scan.current / scan.total) * 100, 100)}%` : '0%' }}
/>
          </div>
        </div>
      )}

      {/* 节点列表 */}
      {nodes.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-24 text-zinc-400">
          <p className="text-base mb-2">未发现节点</p>
          <p className="text-[13px]">
            {isDesktop
              ? '请先在「设置」页配置 Clash 外部控制，或在本页使用订阅自动拉取/导入'
              : '请在本页使用「订阅自动拉取」或「一键拉取并导入」添加节点'}
          </p>
        </div>
      ) : (
        <div className="bg-white rounded-2xl border border-zinc-200 shadow-sm divide-y divide-zinc-100">
          {groups.map(([g, list]) => {
            const isCollapsed = collapsed.has(g)
            const all = groupSelected(list)
            const checkedCount = list.filter((n) => selected.has(n.name)).length
            return (
              <div key={g}>
                {/* 分组行：点击整行（除全选框）展开/收起；全选框 click 冒泡阻止，避免勾选即折叠 */}
                <div
                  onClick={(e) => {
                    // 点击复选框区域不触发折叠（仅全选）
                    if ((e.target as HTMLElement).closest('input[type="checkbox"]')) return
                    toggleGroup(g)
                  }}
                  className="flex items-center gap-3 px-4 py-2.5 bg-zinc-50/50 cursor-pointer select-none"
                >
                  <input
                    type="checkbox"
                    checked={all}
                    onChange={() => toggleGroupSel(list)}
                    onClick={(e) => e.stopPropagation()}
                    disabled={selectable(list).length === 0}
                    className="accent-teal-600 disabled:opacity-30"
                    title={selectable(list).length === 0 ? '该组节点均已添加实例' : ''}
                  />
                  <span className="flex-1 text-left text-[13px] font-semibold text-zinc-700">
                    {g} <span className="text-zinc-400 font-normal">（{list.length}，已选 {checkedCount}）</span>
                  </span>
                  <span className="text-[11px] text-zinc-400">{isCollapsed ? '展开' : '收起'}</span>
                </div>
                {!isCollapsed && (
                  <div className="divide-y divide-zinc-50">
                    {list.map((n) => {
                      const r = resultsMap.get(n.name)
                      const isInstanced = instanceNodes.has(n.name)
                      return (
                        <div
                          key={n.name}
                          onClick={() => {
                            if (!isInstanced) toggleNode(n.name)
                          }}
                          className={clsx(
                            'flex items-center gap-2 px-4 py-2.5 pl-9 transition-colors',
                            // 整行可点（未实例化）才显示手型；已实例化禁选
                            !isInstanced && 'cursor-pointer select-none',
                            // 选中：左侧竖条（inset shadow 不占布局）+ 名称加粗，不做整行大色块（节点挨着时全选会连成一片）
                            selected.has(n.name) && 'shadow-[inset_3px_0_0_0_#0d9488]',
                            // 未选中：hover 浅灰；已实例化（禁选）静息灰底
                            !selected.has(n.name) && isInstanced && 'bg-zinc-50',
                            !selected.has(n.name) && !isInstanced && 'hover:bg-zinc-50',
                          )}
                        >
                          <input
                            type="checkbox"
                            checked={selected.has(n.name)}
                            onChange={() => toggleNode(n.name)}
                            onClick={(e) => e.stopPropagation()}
                            disabled={isInstanced}
                            className="accent-teal-600 disabled:opacity-30"
                          />
                          <div className="flex-1 min-w-0">
                            <div className="flex items-center gap-2">
                              <span className={clsx('text-[13px] truncate', selected.has(n.name) ? 'font-semibold text-teal-800' : 'text-zinc-800')}>{n.name}</span>
                              {isInstanced && (
                                <span
                                  className={clsx(
                                    'inline-block px-1.5 py-0.5 rounded-full text-[10px] font-medium border',
                                    instanceNodes.get(n.name)
                                      ? 'bg-teal-50 text-teal-700 border-teal-100'
                                      : 'bg-blue-50 text-blue-700 border-blue-100',
                                  )}
                                >
                                  {instanceNodes.get(n.name) ? '✓ 已添加到实例池' : '✓ 已添加为独享'}
                                </span>
                              )}
                              <span className="text-[11px] text-zinc-400">{n.node_type}</span>
                              <span className="text-[11px] text-zinc-300 font-mono">{n.server}:{n.port}</span>
                            </div>
                            <div className="text-[11px] text-zinc-400">{n.group}</div>
                          </div>
                          {!n.has_cred && (
                            <span className="text-[11px] text-gray-400" title="该节点缺少连接凭据，扫描时会被跳过">
                              ✗无凭据
                            </span>
                          )}
                          {r && !r.ok && (
                            <span className="text-[11px] text-zinc-400 max-w-[160px] truncate" title={r.message}>
                              {r.message}
                            </span>
                          )}
                          {badgeNode(r)}
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}

    </div>

      {/* N2: 节点池设置弹窗（节点扫描配置折叠面板，默认收起，样式对齐 PoolPage 设置弹窗） */}
      {settingsOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-[722px] max-h-[86vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold text-zinc-900">节点池设置</h2>
              <button onClick={() => setSettingsOpen(false)} className="p-1.5 rounded-lg hover:bg-zinc-100">
                <X size={18} />
              </button>
            </div>

            {/* 节点扫描配置（折叠面板，默认收起） */}
            <section className="border border-zinc-200 rounded-xl overflow-hidden">
              <button
                type="button"
                onClick={() => setOpenSections((s) => ({ ...s, scan: !s.scan }))}
                className={clsx('w-full flex items-center justify-between px-4 py-3 text-left hover:bg-zinc-50', openSections.scan && 'border-b border-zinc-200')}
              >
                <span className="text-[14px] font-semibold text-zinc-900">节点扫描配置</span>
                <ChevronDown size={16} className={clsx('text-zinc-400 transition-transform', !openSections.scan && '-rotate-90')} />
              </button>
              {openSections.scan && (
                <div className="p-4 space-y-4">
                  <p className="text-[12px] text-zinc-400">
                    扫描选中节点 / 停止扫描的并发 worker 数，保存后立即生效（按钮 title 并发数即时更新）。
                  </p>
                  <div className="space-y-2.5 pt-1">
                    <div className="flex items-center gap-3">
                      <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">扫描选中节点（1~16）</label>
                      <input
                        type="number"
                        min={1}
                        max={16}
                        value={scanConf.scan_concurrency}
                        onChange={(e) => setScanConf({ ...scanConf, scan_concurrency: Number(e.target.value) })}
                        className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                      />
                      <span className="w-8 shrink-0" />
                    </div>
                    <div className="flex items-center gap-3">
                      <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">停止扫描（1~8）</label>
                      <input
                        type="number"
                        min={1}
                        max={8}
                        value={scanConf.stop_scan_concurrency}
                        onChange={(e) => setScanConf({ ...scanConf, stop_scan_concurrency: Number(e.target.value) })}
                        className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px] text-right"
                      />
                      <span className="w-8 shrink-0" />
                    </div>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] text-zinc-400">并发过高可能引起进程风暴，建议保持默认</span>
                    <button
                      onClick={() => void handleSaveScanConf()}
                      disabled={savingScanConf}
                      className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:opacity-60 disabled:cursor-not-allowed"
                    >
                      {savingScanConf ? <Loader2 size={14} className="animate-spin" /> : null}
                      {savingScanConf ? '保存中…' : '保存'}
                    </button>
                  </div>
                </div>
              )}
            </section>
          </div>
        </div>
      )}

      {/* T3: 订阅管理弹窗（列表 + 新增 + 拉取 + 删除） */}
      {subOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-[722px] max-h-[86vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold text-zinc-900">订阅管理</h2>
              <button onClick={() => setSubOpen(false)} className="p-1.5 rounded-lg hover:bg-zinc-100">
                <X size={18} />
              </button>
            </div>
            <p className="text-[12px] text-zinc-400">支持 Clash YAML / base64 / v2ray 链接（vmess/vless/trojan/ss/hysteria2），重复节点自动跳过。订阅拉取只进节点池（不自动建实例），需要实例时在节点池勾选后手动添加；后台按每条订阅自己的间隔自动拉取。</p>

            {/* 新增订阅 */}
            <button
              type="button"
              onClick={() => setAddOpen(!addOpen)}
              className="flex items-center gap-1.5 px-3 py-2 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
            >
              <ChevronDown size={14} />
              新增订阅
            </button>
            {addOpen && (
              <div className="border border-zinc-200 rounded-xl p-4 space-y-4">
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">订阅 URL</label>
                  <input
                    type="text"
                    placeholder="https://example.com/sub"
                    value={addUrl}
                    onChange={(e) => setAddUrl(e.target.value)}
                    className="flex-1 min-w-0 px-3 py-2 border rounded-lg text-[13px]"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <label className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap">自动拉取间隔（分钟，0=不自动）</label>
                  <input
                    type="number"
                    min={0}
                    value={addInterval}
                    onChange={(e) => setAddInterval(Number(e.target.value))}
                    className="w-28 shrink-0 px-3 py-2 border rounded-lg text-[13px]"
                  />
                </div>
                <div className="flex items-center gap-3">
                  <label
                    className="text-[13px] text-zinc-700 flex-1 min-w-0 whitespace-nowrap"
                    title="可选：留空则自动用订阅内容配置名 / 文件名 / URL 末段命名分组"
                  >
                    分组名（可选）
                  </label>
                  <input
                    type="text"
                    placeholder="留空自动命名"
                    value={addName}
                    onChange={(e) => setAddName(e.target.value)}
                    className="flex-1 min-w-0 px-3 py-2 border rounded-lg text-[13px]"
                  />
                </div>
                <div className="flex items-center gap-3 pt-1">
                  <button
                    type="button"
                    onClick={() => void handleAddSubscription()}
                    disabled={subBusy}
                    className="flex items-center gap-1.5 bg-green-600 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-green-700 disabled:opacity-60 disabled:cursor-not-allowed"
                  >
                    {subBusy ? <Loader2 size={14} className="animate-spin" /> : null}
                    {subBusy ? '导入中…' : '确定'}
                  </button>
                  <button onClick={() => setAddOpen(false)} className="px-4 py-2 rounded-lg text-[13px] text-zinc-600 hover:bg-zinc-100">
                    取消
                  </button>
                </div>
                {/* P3: 确定按钮等待反馈——不确定进度条 + 已等待 N 秒（10s 看门狗） */}
                {subBusy && (
                  <div className="space-y-1">
                    <div className="h-1.5 bg-zinc-100 rounded-full overflow-hidden relative">
                      <div
                        className="absolute top-0 bottom-0 w-1/3 rounded-full bg-zinc-900"
                        style={{ animation: 'indeterminate-slide 1.8s ease-in-out infinite' }}
                      />
                    </div>
                    <div className="flex justify-between text-[11px] text-zinc-400">
                      <span>正在导入订阅…</span>
                      <span>已等待 {subWaitSec} 秒</span>
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* 订阅列表 */}
            <div className="border border-zinc-200 rounded-xl overflow-hidden">
              <div className="px-4 py-2.5 bg-zinc-50 border-b border-zinc-100 text-[12px] text-zinc-500 flex items-center justify-between">
                <span>我的订阅（{subs.length}）</span>
                <span className="text-[11px] text-zinc-400">间隔 0 = 不自动拉取</span>
              </div>
              {subs.length === 0 ? (
                <div className="py-10 text-center text-[12px] text-zinc-400">暂无订阅，点「新增订阅」添加</div>
              ) : (
                <div className="divide-y divide-zinc-100">
                  {subs.map((s) => (
                    <div key={s.url} className="px-4 py-3 flex items-center gap-3">
                      <div className="flex-1 min-w-0">
                        <div className="text-[13px] text-zinc-800 truncate">{s.url}</div>
                        <div className="text-[11px] text-zinc-400 mt-0.5">
                          {s.group ? (
                            <span className={s.name_pinned ? 'text-zinc-600' : 'text-zinc-400'}>
                              分组：{s.group}
                              {s.name_pinned ? '（固定）' : ''} ·{' '}
                            </span>
                          ) : null}
                          每{s.interval_min > 0 ? `${s.interval_min} 分钟` : '不自动拉取'} · 节点池
                        </div>
                      </div>
                      <button
                        onClick={() => void handleImportSub(s.url)}
                        disabled={importingUrl === s.url}
                        className="flex items-center gap-1 px-2.5 py-1.5 rounded-md text-[12px] text-teal-700 bg-teal-50 border border-teal-100 hover:bg-teal-100 disabled:opacity-60"
                      >
                        {importingUrl === s.url ? <Loader2 size={12} className="animate-spin" /> : <Rss size={12} />}
                        {importingUrl === s.url ? '拉取中…' : '拉取'}
                      </button>
                      <button
                        onClick={() => void handleDeleteSub(s.url)}
                        disabled={deletingUrl === s.url}
                        className="flex items-center gap-1 px-2.5 py-1.5 rounded-md text-[12px] text-red-600 bg-red-50 hover:bg-red-100 disabled:opacity-60"
                      >
                        {deletingUrl === s.url ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
                        {deletingUrl === s.url ? '删除中…' : '删除'}
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      {showResult && (
        <ResultModal
          okCount={okNodes.length}
          total={scan?.total ?? 0}
          busy={acting}
          onClose={() => setShowResult(false)}
          onPool={() => void doCommit('pool')}
          onSolo={() => void doCommit('solo')}
        />
      )}
    </>
  )
}

function badgeNode(r?: ProbeResult) {
  if (!r) return null
  const map: Record<string, [string, string]> = {
    ok: ['bg-green-50 text-green-700', 'ok'],
    upstream: ['bg-amber-50 text-amber-700', 'upstream'],
    config: ['bg-red-50 text-red-600', 'config'],
    socks: ['bg-red-50 text-red-600', 'socks'],
    tls: ['bg-red-50 text-red-600', 'tls'],
    timeout: ['bg-red-50 text-red-600', 'timeout'],
    other: ['bg-zinc-100 text-zinc-500', 'other'],
  }
  const [cl, label] = map[r.category] || ['bg-zinc-100 text-zinc-500', r.category]
  return (
    <span className={clsx('inline-block shrink-0 px-2 py-0.5 rounded-full text-[11px] font-medium', cl)}>
      {label}
      {r.latency_ms > 0 ? ` ${r.latency_ms}ms` : ''}
    </span>
  )
}
