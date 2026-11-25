import { useCallback, useEffect, useState } from 'react'

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

export default function NodePoolCard({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [st, setSt] = useState<PoolStatus | null>(null)
  const [url, setUrl] = useState('')
  const [content, setContent] = useState('')
  const [sel, setSel] = useState<string[]>([])
  const [busy, setBusy] = useState<string | null>(null)
  const [tab, setTab] = useState<'file' | 'url' | 'paste'>('url')
  const [intervalMin, setIntervalMin] = useState('0')
  const [fileName, setFileName] = useState('')

  const load = useCallback(async () => {
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
      setSel(norm.selected)
    }
  }, [])

  useEffect(() => {
    void load().catch(() => undefined)
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
      await load()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
  }

  const apply = async () => {
    setBusy('apply')
    try {
      await post('/api/admin/node-pool/apply', { selected: sel })
      toast('已应用：农场启动并同步到引擎（引擎重启中）', true)
      await load()
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
      await load()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(null)
    }
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
              节点 {st.node_count} · 已选 {st.selected_count}
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
              清空
            </button>
            <span className="ml-auto">端口基址 {st.port_base}</span>
          </div>
          <div className="max-h-64 overflow-auto rounded-lg border border-zinc-200 divide-y divide-zinc-100">
            {st.nodes.map((n) => (
              <label
                key={n.fingerprint}
                className="flex items-center gap-3 px-3 py-2 text-[12px] hover:bg-zinc-50 cursor-pointer"
              >
                <input
                  type="checkbox"
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
              </label>
            ))}
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
          disabled={busy !== null || sel.length === 0}
          className="px-3 py-1.5 rounded-lg text-[13px] text-white bg-teal-600 hover:bg-teal-700 disabled:opacity-40"
        >
          {busy === 'apply' ? '应用中…' : '应用并重启引擎'}
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
        应用后注册请求会轮换走这些本地端口；引擎重启期间免费层可能短暂不可用（30–90 秒）。
      </p>
    </div>
  )
}
