import { useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { Globe, Loader2, RefreshCw, Search, Settings, Trash2 } from 'lucide-react'
import { api, type OrphanProcess } from '../lib/api'
import type { ConfigView, BinariesInfo } from '../lib/api'
import type { UpdateInfo } from '../lib/updates'

export default function SettingsPage({
  toast,
  update,
  checking,
  checkedAt,
  updateError,
  onCheckUpdate,
  onOpenRelease,
  onInstallUpdate,
  canNativeUpdate,
}: {
  toast: (msg: string, ok?: boolean) => void
  update: UpdateInfo | null
  checking: boolean
  checkedAt: number | null
  updateError: string | null
  onCheckUpdate: () => void
  onOpenRelease: (url: string) => void
  onInstallUpdate: () => void
  canNativeUpdate: boolean
}) {
  const [config, setConfig] = useState<ConfigView | null>(null)
  const [autostart, setAutostart] = useState<boolean>(false)
  const [binariesInfo, setBinariesInfo] = useState<BinariesInfo | null>(null)

  // 出口代理（全局唯一维护入口）：设置页编辑，其余页面只勾选「使用代理」
  const [proxyDraft, setProxyDraft] = useState('')
  const [proxyDirty, setProxyDirty] = useState(false)
  const [savingProxy, setSavingProxy] = useState(false)

  // 残留进程清理
  const [orphans, setOrphans] = useState<OrphanProcess[]>([])
  const [orphanBusy, setOrphanBusy] = useState(false)
  const [killBusy, setKillBusy] = useState(false)
  const [selected, setSelected] = useState<Set<number>>(new Set())

  // 忙态
  const [autostartBusy, setAutostartBusy] = useState(false)
  const [cleanLevel, setCleanLevel] = useState<1 | 2 | 3 | null>(null)

  // toast 用 ref 封装（App 的 showToast 每次渲染重建）——effect 固定只跑一次
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
        setProxyDraft((cfg.engine_proxies ?? []).join('\n'))
        setAutostart(as)
        setBinariesInfo(bin)
      } catch (e) {
        console.error('加载设置失败', e)
        toastRef.current('加载设置失败', false)
      }
    }
    void loadData()
  }, [])

  const handleToggleAutostart = async (next: boolean) => {
    setAutostartBusy(true)
    try {
      await api.autostartSet(next)
      setAutostart(next)
      toast(next ? '已开启开机自启' : '已关闭开机自启')
    } catch (e) {
      toast('设置失败：' + (e as Error).message, false)
    } finally {
      setAutostartBusy(false)
    }
  }

  const saveProxy = async () => {
    setSavingProxy(true)
    try {
      await api.configSet('engine_proxies', proxyDraft)
      setProxyDirty(false)
      const cfg = await api.configGet()
      setConfig(cfg)
      toast('出口代理已保存（引擎下次重载生效；已勾选「使用代理」的自定义源自动跟随）')
    } catch (e) {
      toast('保存失败：' + (e as Error).message, false)
    } finally {
      setSavingProxy(false)
    }
  }

  const scanOrphans = async () => {
    setOrphanBusy(true)
    try {
      const r = await api.orphanScan()
      const list = r.items ?? []
      setOrphans(list)
      setSelected(new Set())
      toast(list.length === 0 ? '没有发现残留进程' : `发现 ${list.length} 个残留进程`)
    } catch (e) {
      toast('扫描失败：' + (e as Error).message, false)
    } finally {
      setOrphanBusy(false)
    }
  }

  const killSelected = async () => {
    if (selected.size === 0) return
    setKillBusy(true)
    try {
      const r = await api.orphanKill([...selected])
      const killed = r.killed?.length ?? 0
      const failed = Object.keys(r.errors ?? {}).length
      toast(`已清理 ${killed} 个进程${failed ? `，${failed} 个失败` : ''}`, failed === 0)
      await scanOrphans()
    } catch (e) {
      toast('清理失败：' + (e as Error).message, false)
    } finally {
      setKillBusy(false)
    }
  }

  const doClean = async (level: 1 | 2 | 3) => {
    const label = level === 1 ? '运行数据' : level === 2 ? '运行数据 + 模型缓存' : '全部数据（含配置）'
    if (!window.confirm(`确认清除${label}？此操作不可撤销。`)) return
    setCleanLevel(level)
    try {
      await api.dataClean(level)
      toast(`已清除${label}`)
      const cfg = await api.configGet()
      setConfig(cfg)
    } catch (e) {
      toast('清除失败：' + (e as Error).message, false)
    } finally {
      setCleanLevel(null)
    }
  }

  return (
    <div className="p-6 space-y-4 max-w-4xl">
      <div className="flex items-center gap-2">
        <Settings size={18} className="text-teal-700" />
        <h1 className="text-[16px] font-semibold text-zinc-900">设置</h1>
      </div>

      {/* 服务端口 */}
      <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <h2 className="text-lg font-semibold text-zinc-900">服务端口</h2>
        <p className="mt-1 text-[12px] text-zinc-500">
          管理面板不代理推理请求；推理请分别连 OpenCode（免费层）或供应商服务（自定义源）。
        </p>
        <div className="mt-4 overflow-hidden rounded-xl border border-zinc-100">
          <table className="w-full text-[13px]">
            <thead className="bg-zinc-50 text-zinc-500">
              <tr>
                <th className="text-left font-medium px-3 py-2">服务</th>
                <th className="text-left font-medium px-3 py-2">端口</th>
                <th className="text-left font-medium px-3 py-2">说明</th>
              </tr>
            </thead>
            <tbody className="text-zinc-700">
              <tr className="border-t border-zinc-100">
                <td className="px-3 py-2">管理面板</td>
                <td className="px-3 py-2 font-mono">{window.location.port || '40000'}</td>
                <td className="px-3 py-2 text-zinc-500">当前页面入口（控制面）</td>
              </tr>
              <tr className="border-t border-zinc-100">
                <td className="px-3 py-2">OpenCode 免费层引擎</td>
                <td className="px-3 py-2 font-mono">{config?.gateway_port_effective ?? 40080}</td>
                <td className="px-3 py-2 text-zinc-500">OpenCode 免费模型推理入口</td>
              </tr>
              <tr className="border-t border-zinc-100">
                <td className="px-3 py-2">供应商服务</td>
                <td className="px-3 py-2 font-mono">{config?.provider_port_effective ?? 50080}</td>
                <td className="px-3 py-2 text-zinc-500">自定义源 + 插件供应商入口</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      {/* 出口代理（全局唯一维护入口） */}
      <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2">
          <Globe size={16} className="text-teal-700" />
          <h2 className="text-lg font-semibold text-zinc-900">出口代理</h2>
        </div>
        <p className="mt-1 text-[12px] text-zinc-500">
          全局唯一维护入口：免费层引擎直接使用本列表；自定义供应商只需勾选「使用代理」即可引用（取第一个非 direct 项）。
        </p>
        <textarea
          value={proxyDraft}
          onChange={(e) => {
            setProxyDraft(e.target.value)
            setProxyDirty(true)
          }}
          rows={4}
          placeholder={'direct\nsocks5://127.0.0.1:7897\nhttp://127.0.0.1:7890'}
          spellCheck={false}
          className="mt-3 w-full px-3 py-2 rounded-lg border border-zinc-200 text-[13px] font-mono focus:outline-none focus:ring-2 focus:ring-teal-500/30"
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
            每行一个：http / https / socks5 / socks5h；direct 或留空 = 直连
          </span>
        </div>
      </section>

      {/* 引擎二进制 */}
      <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <h2 className="text-lg font-semibold text-zinc-900">引擎二进制</h2>
        <p className="mt-1 text-[12px] text-zinc-500">
          免费层引擎（opencode2api-win）与核心程序的位置与状态。
        </p>
        <div className="mt-3 space-y-2 text-[13px]">
          <div className="flex items-center gap-2">
            <span
              className={clsx(
                'w-1.5 h-1.5 rounded-full',
                binariesInfo?.oc_exists ? 'bg-green-500' : 'bg-red-500',
              )}
            />
            <span className="text-zinc-700">核心程序 opencode2api</span>
            <span className="text-zinc-400">{binariesInfo?.oc_exists ? '已安装' : '缺失'}</span>
          </div>
          <div className="flex items-center gap-2">
            <span
              className={clsx(
                'w-1.5 h-1.5 rounded-full',
                binariesInfo?.engine_exists ? 'bg-green-500' : 'bg-amber-500',
              )}
            />
            <span className="text-zinc-700">免费层引擎 opencode2api-win</span>
            <span className="text-zinc-400">{binariesInfo?.engine_exists ? '已安装' : '未安装'}</span>
          </div>
          <div className="text-[11px] text-zinc-400">目录：{binariesInfo?.bin_dir ?? '—'}</div>
        </div>
      </section>

      {/* 开机自启 */}
      <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <h2 className="text-lg font-semibold text-zinc-900">开机自启</h2>
        <div className="mt-3 flex items-center gap-3">
          <button
            onClick={() => void handleToggleAutostart(!autostart)}
            disabled={autostartBusy}
            className={clsx(
              'relative w-11 h-6 rounded-full transition-colors',
              autostart ? 'bg-teal-600' : 'bg-zinc-300',
              autostartBusy && 'opacity-60',
            )}
          >
            <span
              className={clsx(
                'absolute top-0.5 w-5 h-5 rounded-full bg-white shadow transition-all',
                autostart ? 'left-[22px]' : 'left-0.5',
              )}
            />
          </button>
          <span className="text-[13px] text-zinc-600">
            {autostart ? '已开启：登录系统后自动启动管理面板' : '已关闭'}
          </span>
        </div>
      </section>

      {/* 残留进程清理 */}
      <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <div className="flex items-center gap-2">
          <h2 className="text-lg font-semibold text-zinc-900">残留进程清理</h2>
          <span className="flex-1" />
          <button
            onClick={() => void scanOrphans()}
            disabled={orphanBusy}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-50"
          >
            <Search size={14} className={orphanBusy ? 'animate-pulse' : ''} />
            {orphanBusy ? '扫描中…' : '扫描'}
          </button>
          <button
            onClick={() => void killSelected()}
            disabled={selected.size === 0 || killBusy}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-red-600 bg-red-50 hover:bg-red-100 disabled:opacity-40"
          >
            {killBusy ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
            清理选中（{selected.size}）
          </button>
        </div>
        <p className="mt-1 text-[12px] text-zinc-500">
          扫描系统中父进程已退出的插件子进程（`--provider-serve`）。本目录的残留由宿主自动回收；此处列出的是其它目录遗留的插件进程。
        </p>
        {orphans.length > 0 ? (
          <div className="mt-3 overflow-hidden rounded-xl border border-zinc-100">
            <table className="w-full text-[13px]">
              <thead className="bg-zinc-50 text-zinc-500">
                <tr>
                  <th className="w-8 px-2 py-2"></th>
                  <th className="text-left font-medium px-3 py-2">PID</th>
                  <th className="text-left font-medium px-3 py-2">进程</th>
                  <th className="text-left font-medium px-3 py-2">端口</th>
                  <th className="text-left font-medium px-3 py-2">说明</th>
                </tr>
              </thead>
              <tbody>
                {orphans.map((p) => (
                  <tr key={p.pid} className="border-t border-zinc-100">
                    <td className="px-2 py-2">
                      <input
                        type="checkbox"
                        checked={selected.has(p.pid)}
                        onChange={(e) => {
                          const n = new Set(selected)
                          if (e.target.checked) n.add(p.pid)
                          else n.delete(p.pid)
                          setSelected(n)
                        }}
                      />
                    </td>
                    <td className="px-3 py-2 font-mono">{p.pid}</td>
                    <td className="px-3 py-2">{p.name || '—'}</td>
                    <td className="px-3 py-2 font-mono">{p.port || '—'}</td>
                    <td className="px-3 py-2 text-zinc-500">{p.detail || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="mt-3 text-[12px] text-zinc-400">尚未扫描，或没有残留进程。</div>
        )}
      </section>

      {/* 清除数据 */}
      <section className="bg-white rounded-2xl border border-red-200 shadow-sm p-5">
        <h2 className="text-lg font-semibold text-red-700">清除数据</h2>
        <p className="mt-1 text-[12px] text-zinc-500">
          分级清理，逐级包含上一级内容。操作不可撤销，建议先备份数据目录。
        </p>
        <div className="mt-3 flex flex-wrap gap-2">
          {([1, 2, 3] as const).map((lv) => (
            <button
              key={lv}
              onClick={() => void doClean(lv)}
              disabled={cleanLevel !== null}
              className={clsx(
                'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] disabled:opacity-40',
                lv === 3
                  ? 'text-white bg-red-600 hover:bg-red-700'
                  : 'text-red-700 bg-red-50 hover:bg-red-100',
              )}
            >
              {cleanLevel === lv ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
              {lv === 1 ? '清运行数据' : lv === 2 ? '清运行数据 + 模型缓存' : '清全部（含配置）'}
            </button>
          ))}
        </div>
      </section>

      {/* 关于 + 更新 */}
      <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
        <h2 className="text-lg font-semibold text-zinc-900">关于</h2>
        <div className="mt-3 space-y-2 text-[13px] text-zinc-600">
          <div>
            当前版本：<span className="font-mono">v{__APP_VERSION__}</span>
          </div>
          {update && (
            <div className="text-[12px] text-zinc-500">
              最新版本：<span className="font-mono">v{update.latest}</span>
            </div>
          )}
          <div className="flex items-center gap-2 pt-1">
            <button
              onClick={onCheckUpdate}
              disabled={checking}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50 disabled:opacity-50"
            >
              <RefreshCw size={14} className={checking ? 'animate-spin' : ''} />
              {checking ? '检查中…' : '检查更新'}
            </button>
            {checkedAt ? (
              <span className="text-[11px] text-zinc-400">
                上次检查：{new Date(checkedAt).toLocaleTimeString()}
              </span>
            ) : null}
            {canNativeUpdate && update ? (
              <button
                onClick={onInstallUpdate}
                className="px-3 py-1.5 rounded-lg text-[13px] text-white bg-teal-600 hover:bg-teal-700"
              >
                立即更新
              </button>
            ) : null}
            {update ? (
              <button
                onClick={() => onOpenRelease(update.url)}
                className="px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 hover:bg-zinc-100"
              >
                查看发布页
              </button>
            ) : null}
          </div>
          {updateError ? (
            <div className="text-[12px] text-red-600">检查失败：{updateError}</div>
          ) : null}
          <div className="pt-2 text-[11px] text-zinc-400">
            OpenCode 免费层由独立的 opencode2api-win 引擎提供（GPL-3.0），以独立进程方式运行。
          </div>
        </div>
      </section>
    </div>
  )
}
