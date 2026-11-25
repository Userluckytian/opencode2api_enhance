import { useEffect, useState } from 'react'
import { Loader2, Waves } from 'lucide-react'
import { api, type ConfigView, type FreeTierEngineStatus, type WarpSample } from '../lib/api'

// 免费层出口池（WARP）：自 2026-11-24 从设置页整体迁到 OpenCode 页。
// 只写 engine_warp_* 配置，生效需要重启引擎（重启按钮在工具栏）。
//
// 状态全部来自父页面的引擎状态轮询（同一个数据源），不在这里另起一套：
// 目标数 / 就绪 / 预注册 / 冷却，以及「重启中」。否则用户永远不知道
// 「保存之后到底注册成功没有」。
export default function WarpPoolCard({
  toast,
  status,
  engineRestarting,
}: {
  toast: (msg: string, ok?: boolean) => void
  status?: FreeTierEngineStatus | null
  engineRestarting?: boolean
}) {
  const [pool, setPool] = useState('4')
  const [srcV6, setSrcV6] = useState('')
  const [relay, setRelay] = useState('')
  const [savedRelay, setSavedRelay] = useState('')
  const [poolManaged, setPoolManaged] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)

  useEffect(() => {
    let alive = true
    void (async () => {
      try {
        const cfg: ConfigView = await api.configGet()
        if (!alive) return
        setPool(String(cfg.engine_warp_pool_size || 4))
        setSrcV6(cfg.engine_warp_source_ipv6 ?? '')
        setRelay(cfg.engine_warp_socks_udp_manual ?? '')
        setSavedRelay(cfg.engine_warp_socks_udp_manual ?? '')
        setPoolManaged(Boolean(cfg.node_pool_enabled))
      } catch {
        // 读不到配置时保持默认值，页面其它区域会显示连接错误
      }
    })()
    return () => {
      alive = false
    }
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      const size = Math.max(1, Math.min(32, parseInt(pool, 10) || 4))
      await api.configSet('engine_warp_pool_size', String(size))
      await api.configSet('engine_warp_source_ipv6', srcV6.trim())
      await api.configSet('engine_warp_socks_udp_manual', relay.trim())
      setPool(String(size))
      setSavedRelay(relay.trim())
      setDirty(false)
      toast(`已保存：出口池 ${size}${relay.trim() ? `，UDP 中继 ${relay.trim()}` : ''}；点顶部「重启」生效`)
    } catch (e) {
      toast('保存失败：' + (e as Error).message, false)
    } finally {
      setSaving(false)
    }
  }

  const resetCooldown = async () => {
    setResetting(true)
    try {
      const r = await api.freeTierResetWarpCooldown()
      toast(`已清除 ${r.cleared} 个冷却出口；需要重启引擎才会重新注册`)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setResetting(false)
    }
  }

  const target = status?.warp_size ?? 0
  const ready = status?.warp_active ?? 0
  const preload = status?.warp_preload ?? 0
  const cooling = status?.warp_cooling ?? 0
  const pendingApply = target > 0 && String(target) !== pool
  const progress = target > 0 ? Math.min(100, Math.round((ready / target) * 100)) : 0
  const history: WarpSample[] = status?.warp_history ?? []

  return (
    <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
      <div className="flex items-center gap-2">
        <Waves size={16} className="text-teal-700" />
        <h2 className="text-lg font-semibold text-zinc-900">免费层出口池（WARP）</h2>
        {engineRestarting ? (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-amber-50 text-amber-700 text-[11px]">
            <Loader2 size={10} className="animate-spin" />
            引擎重启中
          </span>
        ) : null}
      </div>
      <p className="mt-1 text-[12px] text-zinc-500">
        免费层只走匿名通道、没有 key 兜底：每个出口是 Cloudflare 分配的一个独立 /64，额度按 /64
        记账。出口被上游限流会进 24 小时冷却账本，池子会自动补新的。2.0.14 起不再熔断匿名通道——
        失败会立刻返回真实原因（出口被封 / 无可用出口），出口一恢复就能继续用。
      </p>

      {/* 实时池状态：注册成功没有，一眼可见 */}
      <div className="mt-3 rounded-lg border border-zinc-200 px-3 py-2">
        <div className="flex items-center gap-3 text-[12px] text-zinc-700">
          <span>
            目标 <b>{target || '—'}</b>
          </span>
          <span className={ready > 0 ? 'text-emerald-700' : 'text-amber-700'}>
            就绪 <b>{ready}</b>
          </span>
          <span>预注册 <b>{preload}</b></span>
          <span>冷却 <b>{cooling}</b>
          </span>
          <span className="ml-auto text-[11px] text-zinc-400">
            {engineRestarting || status?.state === 'starting'
              ? '注册中…（每个出口 10~90s）'
              : target > 0 && ready < target
                ? '部分出口还在注册/冷却'
                : target > 0
                  ? '全部就绪'
                  : ''}
          </span>
        </div>
        {target > 0 ? (
          <div className="mt-2 h-1.5 rounded bg-zinc-100 overflow-hidden">
            <div
              className={ready > 0 ? 'h-full bg-emerald-500' : 'h-full bg-amber-400'}
              style={{ width: `${progress}%` }}
            />
          </div>
        ) : null}
        {history.length > 1 ? (() => {
          const max = Math.max(
            1,
            ...history.map((h) => Math.max(h.size, h.active, h.cooling)),
          )
          const w = 240
          const hgt = 32
          const path = (vals: number[]) =>
            vals.map((v, i) => `${(i / (vals.length - 1)) * w},${hgt - (v / max) * hgt}`).join(' ')
          return (
            <div className="mt-2">
              <svg viewBox={`0 0 ${w} ${hgt}`} className="w-full h-8" preserveAspectRatio="none">
                <polyline points={path(history.map((h) => h.cooling))} fill="none" stroke="#f59e0b" strokeWidth="1" opacity="0.5" />
                <polyline points={path(history.map((h) => h.active))} fill="none" stroke="#10b981" strokeWidth="1.5" />
              </svg>
              <div className="text-[10px] text-zinc-400">
                近 {history.length} 次巡检：<span className="text-emerald-600">绿=就绪</span> ·{' '}
                <span className="text-amber-600">橙=冷却</span>
              </div>
            </div>
          )
        })() : null}
        {cooling > 0 ? (
          <div className="mt-1.5 text-[11px] text-amber-700">
            冷却中的 /64 额度已烧穿，等冷却到期或换注册源（节点池 / 出口代理）拿新 /64。
          </div>
        ) : null}
      </div>

      <div className="mt-3 grid grid-cols-2 gap-3">
        <label className="space-y-1 block">
          <span className="text-[12px] text-zinc-600">出口池大小（1~32）</span>
          <input
            type="number"
            min={1}
            max={32}
            value={pool}
            onChange={(e) => {
              setPool(e.target.value)
              setDirty(true)
            }}
            className="w-full px-3 py-2 rounded-lg border border-zinc-200 text-[13px]"
          />
        </label>
        <label className="space-y-1 block">
          <span className="text-[12px] text-zinc-600">源 IPv6 前缀（/64，留空 = 不轮换）</span>
          <input
            type="text"
            placeholder="2001:db8:1:2::/64"
            value={srcV6}
            onChange={(e) => {
              setSrcV6(e.target.value)
              setDirty(true)
            }}
            className="w-full px-3 py-2 rounded-lg border border-zinc-200 text-[13px] font-mono"
          />
        </label>
      </div>
      <div className="mt-3">
        <label className="space-y-1 block">
          <span className="text-[12px] text-zinc-600">
            UDP 中继（socks5://host:port，留空 = 用节点池槽位 0 / 直连）
            {poolManaged ? ' · 节点池管理注册源' : ''}
          </span>
          <textarea
            rows={2}
            placeholder={"socks5://127.0.0.1:7890（可多行：按顺序探测，第一行生效）"}
            value={relay}
            onChange={(e) => setRelay(e.target.value)}
            className="w-full px-3 py-2 rounded-lg border border-zinc-200 text-[13px] font-mono disabled:bg-zinc-50 disabled:text-zinc-500"
          />
        </label>
        <div className="mt-1 text-[11px] text-zinc-500">
          本机没有公网 IPv6 时，WireGuard 握手必须经 SOCKS5 UDP 转发。手动值与节点池无关：
          填了就用它（推荐填本地 Clash 的 socks5），留空才用节点池槽位 0。
          当前生效：<span className="font-mono">{status?.warp_socks_udp || '直连'}</span>
          {!poolManaged && relay.trim() !== savedRelay ? '（未保存）' : ''}
          {!poolManaged && relay.trim() === savedRelay && relay.trim() !== (status?.warp_socks_udp ?? '') && relay.trim()
            ? '（重启引擎后生效）'
            : ''}
        </div>
      </div>
      <div className="mt-3 flex items-center gap-2">
        <button
          onClick={() => void save()}
          disabled={(!dirty && relay.trim() === savedRelay) || saving || engineRestarting}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-zinc-900 hover:bg-zinc-800 disabled:opacity-40"
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : null}
          保存
        </button>
        <button
          onClick={() => void resetCooldown()}
          disabled={resetting || engineRestarting}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-orange-700 bg-orange-50 hover:bg-orange-100 disabled:opacity-40"
        >
          {resetting ? <Loader2 size={14} className="animate-spin" /> : null}
          重置出口冷却
        </button>
        <span className="text-[11px] text-zinc-400">
          {engineRestarting
            ? '重启完成后出口会自动重新注册'
            : pendingApply
              ? `配置 ${pool} · 当前引擎 ${target}：保存后点顶部「重启」生效`
              : '改完需重启引擎生效'}
        </span>
      </div>
    </section>
  )
}
