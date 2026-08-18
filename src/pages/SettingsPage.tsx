import { useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { Loader2, Search, Trash2 } from 'lucide-react'
import { api, type OrphanProcess } from '../lib/api'
import type { ConfigView, BinariesInfo } from '../lib/api'
import { isDesktop } from '../lib/env'

export default function SettingsPage({
  toast,
}: {
  toast: (msg: string, ok?: boolean) => void
}) {
  const [config, setConfig] = useState<ConfigView | null>(null)
  const [autostart, setAutostart] = useState<boolean>(false)
  const [binariesInfo, setBinariesInfo] = useState<BinariesInfo | null>(null)

  // Clash 外部控制表单
  const [clashUrl, setClashUrl] = useState('')
  const [clashToken, setClashToken] = useState('')

  // 网关超时切换 / 节点前缀 已归位实例池页
  // 订阅自动拉取 已归位节点池页
  // 残留进程清理（孤儿实例 / 探针残留）
  const [orphans, setOrphans] = useState<OrphanProcess[]>([])
  const [orphanBusy, setOrphanBusy] = useState(false)
  const [killBusy, setKillBusy] = useState(false)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  // P2 audit: 保存 Clash / 保存代理 / 开机自启开关 / 数据清理 忙态
  const [savingClash, setSavingClash] = useState(false)
  const [autostartBusy, setAutostartBusy] = useState(false)
  const [cleanLevel, setCleanLevel] = useState<1 | 2 | 3 | null>(null)


  // G31/L10: toast 用 ref 封装（App 的 showToast 每次渲染重建）——effect 固定只跑一次，
  // 不因 toast 变化重发 3 个请求，也不会把服务器旧值覆盖回未保存的表单输入
  const toastRef = useRef(toast)
  toastRef.current = toast

  useEffect(() => {
    const loadData = async () => {
      try {
        const [cfg, as, bin] = await Promise.all([
          api.configGet(),
          api.autostartGet(),
          api.getBinariesInfo(),
        ])
        setConfig(cfg)
        setAutostart(as)
        setBinariesInfo(bin)
      } catch (e) {
        console.error('加载设置失败', e)
        toastRef.current('加载设置失败', false)
      }
    }
    loadData()
    // L10: 首次加载初始化表单值（clashUrl）——单独处理、不回写进 loadData，
    // 后续其它路径重载数据也不会覆盖用户未保存的表单输入
    api
      .configGet()
      .then((cfg) => {
        setClashUrl(cfg.clash_external_url)
      })
      .catch(() => {})
  }, [])

  const handleSaveClash = async () => {
    setSavingClash(true)
    try {
      await api.configSet('clash_external_url', clashUrl)
      if (clashToken.trim()) {
        await api.configSet('clash_auth_token', clashToken)
      }
      toast('已保存', true)
      // 重新加载配置以更新 has_clash_token 状态
      const cfg = await api.configGet()
      setConfig(cfg)
      setClashToken('')
    } catch (e) {
      console.error('保存失败', e)
      toast('保存失败', false)
    } finally {
      setSavingClash(false)
    }
  }

  const handleAutostartChange = async (enabled: boolean) => {
    if (autostartBusy) return
    setAutostartBusy(true)
    try {
      await api.autostartSet(enabled)
      setAutostart(enabled)
      toast(enabled ? '已启用开机自启' : '已禁用开机自启', true)
    } catch (e) {
      console.error('设置开机自启失败', e)
      toast('设置失败', false)
    } finally {
      setAutostartBusy(false)
    }
  }

  // 统一网关密钥（main 功能 M6）已在实例池页提供（同一 gateway_key，避免两处重复入口）——此处不再保留

  // 残留进程：探测 / 全选 / 一键清除
  const doScanOrphans = async () => {
    setOrphanBusy(true)
    try {
      const s = await api.orphanScan()
      setOrphans(s.items)
      setSelected(new Set(s.items.map((i) => i.pid)))
      toast(`探测到 ${s.total} 个残留进程（探针 ${s.probe} · 孤儿 ${s.orphan}）`, s.total === 0)
    } catch (e) {
      console.error('探测残留进程失败', e)
      toast('探测失败', false)
    } finally {
      setOrphanBusy(false)
    }
  }

  const toggleAll = () => {
    setSelected((prev) => (prev.size === orphans.length ? new Set() : new Set(orphans.map((i) => i.pid))))
  }

  const toggleOne = (pid: number) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(pid)) next.delete(pid)
      else next.add(pid)
      return next
    })
  }

  const doKillOrphans = async () => {
    const pids = [...selected]
    if (pids.length === 0) {
      toast('未选中任何进程', false)
      return
    }
    if (!window.confirm(`确定清除选中的 ${pids.length} 个残留进程？\n运行中的实例与网关不受影响。`)) return
    setKillBusy(true)
    try {
      const r = await api.orphanKill(pids)
      const errCount = Object.keys(r.errors).length
      toast(`已清除 ${r.killed.length} 个残留进程${errCount ? `，失败 ${errCount}` : ''}`, errCount === 0)
      await doScanOrphans() // 清除后自动重新探测
    } catch (e) {
      console.error('清除残留进程失败', e)
      toast('清除失败', false)
    } finally {
      setKillBusy(false)
    }
  }

  const handleDataClean = async (level: 1 | 2 | 3) => {
    const labels: Record<number, string> = {
      1: '仅清理运行时数据（日志、统计、临时配置，保留实例记录）',
      2: '清理运行时数据 + 清空实例记录（回到空实例池）',
      3: '全部重置（运行数据 + 实例 + 配置，回到出厂默认）',
    }
    if (!window.confirm(`确定要执行「${labels[level]}」？\n\n这会先停止所有运行中的实例与网关。此操作不可撤销。`)) return
    if (level === 3 && !window.confirm('这是完全重置，将删除所有配置并备份到 config.json.bak。\n请再次确认继续？')) return
    if (cleanLevel) return
    setCleanLevel(level)
    try {
      await api.dataClean(level)
      try {
        const [cfg, as] = await Promise.all([api.configGet(), api.autostartGet()])
        setConfig(cfg)
        setAutostart(as)
      } catch { /* 忽略刷新失败 */ }
      toast('清理完成', true)
    } catch (e) {
      console.error('清理失败', e)
      toast('清理失败', false)
    } finally {
      setCleanLevel(null)
    }
  }

  if (!config || !binariesInfo) {
    return <div className="p-8 text-zinc-500">加载中...</div>
  }

  return (
    <div className="p-6 space-y-6 max-w-2xl mx-auto">
      <h1 className="text-2xl font-semibold text-zinc-900">设置</h1>

      {/* Clash 外部控制（仅桌面端：本机有 Clash 客户端才有意义；Web/Docker/Linux 服务器 headless 端隐藏，走订阅导入） */}
      {isDesktop && (
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">Clash 外部控制</h2>
        
        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">URL</label>
          <input
            type="text"
            placeholder="http://127.0.0.1:9097"
            value={clashUrl}
            onChange={(e) => setClashUrl(e.target.value)}
            className="w-full px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">Clash 控制面板的访问地址</p>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">访问密钥</label>
          <input
            type="password"
            placeholder={config.has_clash_token ? '留空则不修改' : ''}
            value={clashToken}
            onChange={(e) => setClashToken(e.target.value)}
            className="w-full px-3 py-2 border rounded-lg"
          />
          {config.has_clash_token && (
            <p className="text-zinc-500 text-xs">已配置</p>
          )}
          <p className="text-zinc-500 text-xs">留空则不修改</p>
        </div>

        <button
          onClick={handleSaveClash}
          disabled={savingClash}
          className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {savingClash ? <Loader2 size={14} className="animate-spin" /> : null}
          {savingClash ? '保存中…' : '保存'}
        </button>
      </div>
      )}

      {/* 开机自启（仅桌面端：桌面临近登录自启；Web/Docker/Linux 服务器 headless 端隐藏，服务器用 systemd/容器编排管理） */}
      {isDesktop && (
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">开机自启</h2>
        
        <div className="flex items-center space-x-3">
          <label className="relative inline-flex items-center cursor-pointer">
            <input
              type="checkbox"
              checked={autostart}
              onChange={(e) => handleAutostartChange(e.target.checked)}
              disabled={autostartBusy}
              className="sr-only peer"
            />
            <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
          </label>
          <span className="text-sm text-zinc-700">开机时自动启动管理器</span>
        </div>
        <p className="text-zinc-500 text-xs">Windows 注册表 / Linux .desktop / macOS LaunchAgent</p>
      </div>
      )}

      {/* 残留进程清理（孤儿实例 / 探针残留） */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h2 className="text-lg font-medium text-zinc-900">残留进程清理</h2>
            <p className="text-zinc-500 text-xs">
              探测「占着进程但未使用」的节点/实例/探针残留（扫描残留、已停止实例的孤儿进程），勾选后一键清除；运行中的实例与网关自动跳过。
            </p>
          </div>
          <button
            onClick={() => void doScanOrphans()}
            disabled={orphanBusy}
            className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700 disabled:opacity-60 disabled:cursor-not-allowed whitespace-nowrap"
          >
            {orphanBusy ? <Loader2 size={14} className="animate-spin" /> : <Search size={14} />}
            {orphanBusy ? '探测中…' : '探测残留'}
          </button>
        </div>

        {orphans.length > 0 && (
          <>
            <div className="rounded-lg border border-zinc-200 overflow-hidden">
              <table className="w-full text-[13px]">
                <thead>
                  <tr className="text-left text-zinc-400 bg-zinc-50 border-b border-zinc-100">
                    <th className="py-2 pl-3 w-8">
                      <input
                        type="checkbox"
                        checked={selected.size === orphans.length && orphans.length > 0}
                        onChange={toggleAll}
                        title="全选/取消"
                      />
                    </th>
                    <th className="py-2 pl-2">进程</th>
                    <th className="py-2 pl-2">PID</th>
                    <th className="py-2 pl-2">类型</th>
                    <th className="py-2 pl-2 pr-3">说明</th>
                  </tr>
                </thead>
                <tbody>
                  {orphans.map((o) => (
                    <tr key={o.pid} className="border-b border-zinc-50 hover:bg-zinc-50/60">
                      <td className="py-2 pl-3">
                        <input type="checkbox" checked={selected.has(o.pid)} onChange={() => toggleOne(o.pid)} />
                      </td>
                      <td className="py-2 pl-2 text-zinc-700 font-mono">{o.name}</td>
                      <td className="py-2 pl-2 text-zinc-500">{o.pid}</td>
                      <td className="py-2 pl-2">
                        <span
                          className={clsx(
                            'inline-block px-2 py-0.5 rounded-full text-[11px] font-medium',
                            o.category === 'probe' ? 'bg-orange-50 text-orange-600' : 'bg-amber-50 text-amber-700',
                          )}
                        >
                          {o.category === 'probe' ? '探针残留' : '实例残留'}
                        </span>
                        {o.instance && <span className="ml-1.5 text-[12px] text-zinc-500">{o.instance}</span>}
                        {o.port! > 0 && <span className="ml-1.5 text-[12px] text-zinc-400">端口 {o.port}</span>}
                      </td>
                      <td className="py-2 pl-2 pr-3 text-zinc-500">{o.detail}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="flex items-center justify-end gap-3">
              <span className="text-[12px] text-zinc-400">
                已选 {selected.size} / {orphans.length}
              </span>
              <button
                onClick={() => void doKillOrphans()}
                disabled={killBusy || selected.size === 0}
                className="flex items-center gap-1.5 bg-red-600 text-white rounded-lg px-4 py-2 text-sm hover:bg-red-700 disabled:opacity-60 disabled:cursor-not-allowed"
              >
                {killBusy ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
                {killBusy ? '清除中…' : '一键清除'}
              </button>
            </div>
          </>
        )}
      </div>

      {/* 清除数据 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4 border-red-200">
        <h2 className="text-lg font-medium text-red-700">清除数据</h2>
        <p className="text-zinc-500 text-xs">
          遇到环境异常（实例/端口残留、配置损坏）时可清理本地数据。执行前会自动停止所有实例与网关。
        </p>
        <div className="flex flex-wrap gap-2">
          <button
            onClick={() => handleDataClean(1)}
            disabled={!!cleanLevel}
            className="flex items-center gap-1.5 px-4 py-2 rounded-lg border border-zinc-300 text-sm hover:bg-zinc-100 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {cleanLevel === 1 ? <Loader2 size={14} className="animate-spin" /> : null}
            清理运行数据
          </button>
          <button
            onClick={() => handleDataClean(2)}
            disabled={!!cleanLevel}
            className="flex items-center gap-1.5 px-4 py-2 rounded-lg border border-amber-300 text-sm text-amber-700 hover:bg-amber-50 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {cleanLevel === 2 ? <Loader2 size={14} className="animate-spin" /> : null}
            清空实例记录
          </button>
          <button
            onClick={() => handleDataClean(3)}
            disabled={!!cleanLevel}
            className="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-red-600 text-white text-sm hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {cleanLevel === 3 ? <Loader2 size={14} className="animate-spin" /> : null}
            全部重置
          </button>
        </div>
        <p className="text-zinc-500 text-xs">全部重置会删除 config.json（备份为 config.json.bak），需重新配置</p>
      </div>

      {/* 关于 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">关于</h2>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">版本</label>
          <code className="block text-sm bg-zinc-100 px-3 py-2 rounded border font-mono">
            v{__APP_VERSION__}
          </code>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">二进制目录</label>
          <code className="block text-sm bg-zinc-100 px-3 py-2 rounded border font-mono">
            {binariesInfo.bin_dir}
          </code>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">子程序状态</label>
          <div className="space-y-1">
            <div className="flex items-center space-x-2 text-sm">
              <span className={binariesInfo.oc_exists ? 'text-green-600' : 'text-red-600'}>
                {binariesInfo.oc_exists ? '✓' : '✗'}
              </span>
              <span>opencode2api.exe</span>
            </div>
            <div className="flex items-center space-x-2 text-sm">
              <span className={binariesInfo.sb_exists ? 'text-green-600' : 'text-red-600'}>
                {binariesInfo.sb_exists ? '✓' : '✗'}
              </span>
              <span>sing-box.exe</span>
            </div>
          </div>
        </div>

        <p className="text-zinc-500 text-xs">子程序随主程序内嵌，运行时不满足时自动释放</p>
      </div>
    </div>
  )
}