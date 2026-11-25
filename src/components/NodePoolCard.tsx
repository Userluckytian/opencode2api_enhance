import { useCallback, useEffect, useState } from 'react'
import { Trash2 } from 'lucide-react'
import type { FreeTierEngineStatus } from '../lib/api'

// WARP 注册节点池：订阅 → 本地端口 → engine_warp_registration_proxies。
// 只服务 OpenCode 免费层注册；设置里的「出口代理」不受影响。

type PoolNode = {
  name: string
  type: string
  server: string
  port: number
  udp: boolean
  fingerprint: string
}

type PoolStatus = {
  enabled: boolean
  running: boolean
  pid: number
  port_base: number
  ports: number[]
  nodes: PoolNode[]
  selected: string[]
  node_count: number
  selected_count: number
  /** 被移除（屏蔽）的节点数：订阅刷新不会复活 */
  hidden_count?: number
  /** 固定注册槽位数（0 = 农场未按固定槽位运行） */
  slots?: number
  /** 定时巡检：最近一次时间与不可达槽位数 */
  health_checked_at?: string
  health_dead?: number
  /** 槽位 i 对应的节点指纹（与引擎 warp.registrars 的 index 对齐） */
  slots_nodes?: string[]
  mihomo_path: string
  socks_udp?: string
  warning?: string
  last_error?: string
  updated_at?: string
  synced_to?: string
}

const post = async <T,>(path: string, body: unknown): Promise<T> => {
  const r = await fetch(path, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const text = await r.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = text
  }
  if (!r.ok) {
    const msg =
      data && typeof data === 'object' && 'error' in (data as Record<string, unknown>)
        ? String((data as Record<string, unknown>).error)
        : `HTTP ${r.status}`
    throw new Error(msg)
  }
  return data as T
}

export default function NodePoolCard({
  toast,
  onEngineRestarting,
  engineRestarting,
  engineStatus,
}: {
  toast: (msg: string, ok?: boolean) => void
  /** 应用成功会触发引擎重启：通知父页面立刻切到「重启中」 */
  onEngineRestarting?: () => void
  /** 父页面判定的重启态（本卡片据此禁用按钮并显示进度） */
  engineRestarting?: boolean
  /** 引擎状态：提供每槽位（= 每节点）的注册成败 */
  engineStatus?: FreeTierEngineStatus | null
}) {
  const [st, setSt] = useState<PoolStatus | null>(null)
  const [url, setUrl] = useState('')
  const [content, setContent] = useState('')
  const [sel, setSel] = useState<string[]>([])
  const [busy, setBusy] = useState<string | null>(null)
  const [tab, setTab] = useState<'file' | 'url' | 'paste'>('url')
  const [intervalMin, setIntervalMin] = useState('0')
  const [fileName, setFileName] = useState('')

  // replaceSelection=true 时才用服务端已选覆盖本地勾选。
  // 轮询不能动本地选择：4s 一次的 setSel(服务端值) 会把用户刚点的
  // 「全选 / 只勾选 UDP」瞬间弹回去（2.0.14 引入轮询时的回归）。
  const load = useCallback(async (replaceSelection = false) => {
    const r = await fetch('/api/admin/node-pool/status', { credentials: 'include' })
    if (r.ok) {
      const s = (await r.json()) as PoolStatus
      // 后端空切片会序列化成 null（Go nil slice），这里统一归一成数组，
      // 否则渲染时 st.nodes.length / st.ports.length 会抛 TypeError 把整站拖白。
      const norm: PoolStatus = {
        ...s,
        ports: s.ports ?? [],
        nodes: s.nodes ?? [],
        selected: s.selected ?? [],
      }
      setSt(norm)
      if (replaceSelection) setSel(norm.selected)
    }
  }, [])

  useEffect(() => {
    void load(true).catch(() => undefined)
    // 农场/引擎会在后台自发变化（重启、自动刷新订阅），卡片不能只靠手点刷新。
    const t = window.setInterval(() => void load().catch(() => undefined), 4000)
    return () => window.clearInterval(t)
  }, [load])

  const importSub = async () => {
    setBusy('import')
    try {
      if (tab === 'url') {
        // 刷新间隔跟着导入一起保存（0 = 不自动刷新）。
        await post('/api/admin/config/set', {
          key: 'node_pool_refresh_min',
          value: intervalMin || '0',
        }).catch(() => undefined)
      }
      const res = await post<{ node_count: number; bad: number }>('/api/admin/node-pool/import', {
        url: tab === 'url' ? url : '',
        content: tab === 'url' ? '' : content,
      })
      toast(`导入成功：${res.node_count} 个节点${res.bad ? `，${res.bad} 个解析失败` : ''}`, true)
      await load(true)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const apply = async () => {
    setBusy('apply')
    try {
      const r = await post<{ hot?: boolean }>('/api/admin/node-pool/apply', { selected: sel })
      // hot = 只热重载了 mihomo 的槽位映射，引擎没重启：加/换节点不再断服务。
      if (!r.hot) onEngineRestarting?.()
      toast(
        r.hot
          ? '已应用：节点映射已热重载（引擎未重启，下一次出口注册走新节点）'
          : '已应用：首次启用/中继变化，引擎正在重启（WARP 重新注册出口，最长 90s）',
        true,
      )
      await load(true)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const disable = async () => {
    setBusy('stop')
    try {
      await post('/api/admin/node-pool/stop', {})
      toast('已停用节点池，注册回退到出口代理', true)
      await load(true)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const removeNodes = async (fps: string[], all = false) => {
    if (!all && fps.length === 0) return
    const tip = all ? '清空全部节点并重置屏蔽名单？' : `移除 ${fps.length} 个节点？`
    if (!window.confirm(`${tip}

移除只改本地列表（订阅刷新也不会复活），已选中的节点需要重新「应用」才会从引擎注册表里去掉。`)) return
    setBusy('remove')
    try {
      const r = await post<{ removed: number; remain: number }>('/api/admin/node-pool/remove', {
        fingerprints: fps,
        all,
      })
      toast(all ? `已清空节点（原 ${r.removed} 个）` : `已移除 ${r.removed} 个节点，剩余 ${r.remain} 个`, true)
      await load(true)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  // 节点注册状态：把引擎的 per-registrar 统计按「槽位 → 节点指纹」聚合回来。
  // 槽位固定 8 个，选中超过 8 个时只有前 8 个进入轮换，其余显示「备用」。
  const regOf = (fp: string) => {
    const idxs = (st?.slots_nodes ?? [])
      .map((x, i) => (x === fp ? i : -1))
      .filter((i) => i >= 0)
    if (idxs.length === 0) {
      return {
        label: '备用',
        cls: 'bg-zinc-100 text-zinc-500',
        title: '未进入注册槽位（固定 8 个槽位，按列表顺序取前 8 个节点）',
      }
    }
    let attempts = 0
    let successes = 0
    let failures = 0
    let lastErr = ''
    for (const i of idxs) {
      const r = (engineStatus?.warp_registrars ?? []).find((x) => x.index === i)
      if (!r) continue
      attempts += r.attempts
      successes += r.successes
      failures += r.failures
      if (r.last_error) lastErr = r.last_error
    }
    const starting = Boolean(engineStatus?.restarting) || engineStatus?.state === 'starting'
    if (attempts === 0) {
      return starting
        ? { label: '注册中…', cls: 'bg-amber-50 text-amber-700', title: '引擎正在注册 WARP 出口' }
        : { label: '待注册', cls: 'bg-zinc-100 text-zinc-600', title: '槽位已就绪，下一次出口注册会走这个节点' }
    }
    if (failures > 0 && successes === 0) {
      return { label: `失败 ${failures}`, cls: 'bg-red-50 text-red-600', title: lastErr || '注册失败' }
    }
    if (successes > 0 && failures > 0) {
      return {
        label: `成功 ${successes} / 失败 ${failures}`,
        cls: 'bg-amber-50 text-amber-800',
        title: lastErr,
      }
    }
    return { label: `成功 ${successes}`, cls: 'bg-emerald-50 text-emerald-700', title: '' }
  }

  const toggle = (fp: string) => {
    setSel((prev) => (prev.includes(fp) ? prev.filter((x) => x !== fp) : [...prev, fp]))
  }

  const selectUDP = () => {
    if (!st) return
    setSel(st.nodes.filter((n) => n.udp).map((n) => n.fingerprint))
  }

  const badges = st?.running ? (
    <span className="px-2 py-0.5 rounded-full bg-teal-50 text-teal-700 text-[11px]">
      农场运行中 · {st.ports.length} 端口
    </span>
  ) : (
    <span className="px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-600 text-[11px]">农场未启动</span>
  )

  return (
    <div className="rounded-xl border border-zinc-200 bg-white p-5 space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-[14px] font-semibold text-zinc-900">WARP 注册节点池（订阅）</h2>
            {badges}
            {st?.synced_to ? (
              <span className="px-2 py-0.5 rounded-full bg-emerald-50 text-emerald-700 text-[11px]">
                已同步到引擎
              </span>
            ) : null}
          </div>
          <p className="text-[12px] text-zinc-500 mt-1">
            订阅节点只用于 WARP 注册，不参与业务转发；业务出口仍由设置里的「出口代理」决定。
          </p>
        </div>
        <div className="text-[11px] text-zinc-500 text-right whitespace-nowrap">
          {st ? (
            <>
              节点 {st.node_count} · 已选 {sel.length}
              {sel.join(',') !== (st.selected ?? []).join(',') ? '（未应用）' : ''}
              <br />
              {st.updated_at ? `更新于 ${st.updated_at}` : '尚未导入'}
            </>
          ) : null}
        </div>
      </div>

      <div className="flex items-center gap-2">
        <button
          onClick={() => void importSub()}
          disabled={busy !== null || (tab === 'url' ? !url.trim() : !content.trim())}
          className="px-3 py-1.5 rounded-lg text-[13px] text-teal-700 bg-teal-50 hover:bg-teal-100 disabled:opacity-40"
        >
          {busy === 'import' ? '导入中…' : '导入 / 刷新'}
        </button>
      </div>

      <div className="flex items-center gap-1 border-b border-zinc-200">
        {(
          [
            ['file', '本地文件'],
            ['url', 'URL 订阅'],
            ['paste', '粘贴内容'],
          ] as const
        ).map(([key, label]) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            className={
              tab === key
                ? 'px-3 py-1.5 text-[13px] font-medium text-teal-700 border-b-2 border-teal-600 -mb-px'
                : 'px-3 py-1.5 text-[13px] text-zinc-500 hover:text-zinc-700'
            }
          >
            {label}
          </button>
        ))}
      </div>

      {tab === 'file' ? (
        <div className="text-[12px] text-zinc-600 space-y-2">
          <input
            type="file"
            accept=".yaml,.yml,.txt,.json"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (!f) return
              setFileName(f.name)
              const reader = new FileReader()
              reader.onload = () => setContent(String(reader.result ?? ''))
              reader.readAsText(f)
            }}
            className="block w-full text-[12px]"
          />
          <div className="text-[11px] text-zinc-400">
            {fileName ? `已读取：${fileName}` : '选择 Clash 配置（YAML）或导出的节点文本'}
          </div>
        </div>
      ) : null}

      {tab === 'url' ? (
        <div className="space-y-2">
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="Clash 订阅 URL（支持 http/https）"
            className="w-full px-3 py-1.5 rounded-lg border border-zinc-200 text-[13px]"
          />
          <div className="flex items-center gap-2 text-[12px] text-zinc-600">
            <span>自动刷新间隔</span>
            <input
              value={intervalMin}
              onChange={(e) => setIntervalMin(e.target.value.replace(/[^0-9]/g, ''))}
              className="w-20 px-2 py-1 rounded border border-zinc-200 text-[12px]"
            />
            <span>分钟（0 = 不自动刷新）</span>
          </div>
        </div>
      ) : null}

      {tab === 'paste' ? (
        <textarea
          value={content}
          onChange={(e) => setContent(e.target.value)}
          placeholder="把 Clash 配置 YAML 粘到这里（含 proxies: 段）"
          className="w-full h-28 px-3 py-2 rounded-lg border border-zinc-200 font-mono text-[11px]"
        />
      ) : null}

      {st && st.nodes.length > 0 ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-[12px] text-zinc-600">
            <button
              onClick={selectUDP}
              className="px-2 py-1 rounded bg-zinc-100 hover:bg-zinc-200"
            >
              只勾选 UDP 可用
            </button>
            <button
              onClick={() => setSel(st.nodes.map((n) => n.fingerprint))}
              className="px-2 py-1 rounded bg-zinc-100 hover:bg-zinc-200"
            >
              全选
            </button>
            <button onClick={() => setSel([])} className="px-2 py-1 rounded bg-zinc-100 hover:bg-zinc-200">
              全不选
            </button>
            <button
              onClick={() => void removeNodes([], true)}
              disabled={busy !== null || engineRestarting}
              className="px-2 py-1 rounded bg-red-50 text-red-600 hover:bg-red-100 disabled:opacity-40"
              title="清空节点列表并重置屏蔽名单（之后重新导入可恢复全部）"
            >
              {busy === 'remove' ? '处理中…' : '清空列表'}
            </button>
            <span className="ml-auto">
              端口基址 {st.port_base} · 固定槽位 {st.slots || st.ports.length || '—'}
              {st.health_checked_at
                ? ` · 巡检 ${st.health_checked_at}${st.health_dead ? `（不可达 ${st.health_dead}）` : ''}`
                : ''}
            </span>
          </div>
          {(st.hidden_count ?? 0) > 0 ? (
            <div className="text-[11px] text-zinc-500">
              已屏蔽 {st.hidden_count} 个被移除的节点：订阅刷新不会复活，点「清空列表」可重置屏蔽
            </div>
          ) : null}
          <div className="max-h-64 overflow-auto rounded-lg border border-zinc-200 divide-y divide-zinc-100">
            {st.nodes.map((n) => {
              const reg = regOf(n.fingerprint)
              return (
              <div
                key={n.fingerprint}
                className="flex items-center gap-3 px-3 py-2 text-[12px] hover:bg-zinc-50"
              >
                <input
                  type="checkbox"
                  className="cursor-pointer"
                  checked={sel.includes(n.fingerprint)}
                  onChange={() => toggle(n.fingerprint)}
                />
                <span className="font-medium text-zinc-800 flex-1 truncate">{n.name}</span>
                <span className="text-zinc-500 uppercase">{n.type}</span>
                <span className="text-zinc-500 truncate max-w-[180px]">
                  {n.server}:{n.port}
                </span>
                <span
                  className={
                    n.udp
                      ? 'px-1.5 py-0.5 rounded bg-emerald-50 text-emerald-700'
                      : 'px-1.5 py-0.5 rounded bg-zinc-100 text-zinc-500'
                  }
                  title={n.udp ? '节点支持 UDP，WARP 握手成功率高' : '节点不支持 UDP，WARP 握手可能失败'}
                >
                  UDP {n.udp ? '✓' : '✗'}
                </span>
                {/* 注册状态列：数据来自引擎的 per-registrar 统计（2.0.16 补丁） */}
                <span className={`px-1.5 py-0.5 rounded whitespace-nowrap ${reg.cls}`} title={reg.title}>
                  {reg.label}
                </span>
                <button
                  onClick={() => void removeNodes([n.fingerprint])}
                  disabled={busy !== null || engineRestarting}
                  className="px-1 py-0.5 rounded text-zinc-400 hover:text-red-600 hover:bg-red-50 disabled:opacity-30"
                  title="从列表移除（不再参与注册；订阅刷新不会复活）"
                >
                  <Trash2 size={13} />
                </button>
              </div>
              )
            })}
          </div>
          {st.ports.length > 0 ? (
            <div className="text-[11px] text-zinc-500 font-mono break-all">
              已分配端口：{st.ports.map((p) => `127.0.0.1:${p}`).join('  ')}
            </div>
          ) : null}
          {st.socks_udp ? (
            <div className="text-[11px] text-teal-700 font-mono break-all">
              WARP 握手中继：{st.socks_udp}（UDP 经该节点转发）
            </div>
          ) : null}
        </div>
      ) : (
        <div className="text-[12px] text-zinc-500">还没有导入节点。请填写订阅地址后点「导入 / 刷新」。</div>
      )}

      {st?.warning ? (
        <div className="text-[12px] text-amber-800 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">
          {st.warning}
        </div>
      ) : null}

      {st?.last_error ? (
        <div className="text-[12px] text-amber-700 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">
          {st.last_error}
        </div>
      ) : null}

      {st && !st.mihomo_path ? (
        <div className="text-[12px] text-red-600">
          未找到 mihomo：把 mihomo.exe 放到 bin/ 目录，或在设置里填写 node_pool_mihomo_path。
        </div>
      ) : null}

      <div className="flex items-center gap-2">
        <button
          onClick={() => void apply()}
          disabled={busy !== null || engineRestarting || sel.length === 0}
          className="px-3 py-1.5 rounded-lg text-[13px] text-white bg-teal-600 hover:bg-teal-700 disabled:opacity-40"
        >
          {busy === 'apply'
            ? '应用中…'
            : engineRestarting
              ? '引擎重启中…'
              : '应用节点（注册 WARP 出口）'}
        </button>
        <button
          onClick={() => void disable()}
          disabled={busy !== null || !st?.enabled}
          className="px-3 py-1.5 rounded-lg text-[13px] text-red-600 bg-red-50 hover:bg-red-100 disabled:opacity-40"
        >
          停用并回退
        </button>
        <button
          onClick={() => void load()}
          disabled={busy !== null}
          className="px-3 py-1.5 rounded-lg text-[13px] text-zinc-700 bg-white border border-zinc-200 hover:bg-zinc-50"
        >
          刷新状态
        </button>
      </div>
      <p className="text-[11px] text-zinc-400">
        增删/替换节点只热重载 mihomo 的「槽位 → 节点」映射，不重启引擎（下一次出口注册生效）；
        只有首次启用、停用，或 UDP 握手中继从无到有/从有到无时，引擎才会重启一次。
      </p>
    </div>
  )
}
