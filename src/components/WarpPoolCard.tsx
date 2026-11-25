import { useEffect, useState } from 'react'
import { Loader2, Waves } from 'lucide-react'
import { api, type ConfigView } from '../lib/api'

// 免费层出口池（WARP）：自 2026-11-24 从设置页整体迁到 OpenCode 页。
// 只写 engine_warp_* 配置，生效需要重启引擎（重启按钮在工具栏）。
export default function WarpPoolCard({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [pool, setPool] = useState('4')
  const [srcV6, setSrcV6] = useState('')
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
      setPool(String(size))
      setDirty(false)
      toast('出口池已保存（重启引擎后重新注册出口）')
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
      toast(`已清除 ${r.cleared} 个冷却出口，请重启引擎让它重新注册出口`)
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setResetting(false)
    }
  }

  return (
    <section className="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5">
      <div className="flex items-center gap-2">
        <Waves size={16} className="text-teal-700" />
        <h2 className="text-lg font-semibold text-zinc-900">免费层出口池（WARP）</h2>
      </div>
      <p className="mt-1 text-[12px] text-zinc-500">
        免费层只走匿名通道、没有 key 兜底：每个出口是 Cloudflare 分配的一个独立 /64，额度按 /64
        记账。出口被上游限流会进 24 小时冷却账本，池子会自动补新的；连续失败 6 次会让匿名通道熔断（1h
        起，逐次翻倍至 24h）。
      </p>
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
      <div className="mt-3 flex items-center gap-2">
        <button
          onClick={() => void save()}
          disabled={!dirty || saving}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-white bg-zinc-900 hover:bg-zinc-800 disabled:opacity-40"
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : null}
          保存
        </button>
        <button
          onClick={() => void resetCooldown()}
          disabled={resetting}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[13px] text-orange-700 bg-orange-50 hover:bg-orange-100 disabled:opacity-40"
        >
          {resetting ? <Loader2 size={14} className="animate-spin" /> : null}
          重置出口冷却
        </button>
        <span className="text-[11px] text-zinc-400">改完需重启引擎生效</span>
      </div>
    </section>
  )
}
