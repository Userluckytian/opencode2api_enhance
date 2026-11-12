import { useCallback, useEffect, useState } from 'react'
import clsx from 'clsx'
import { Copy, Loader2, Pencil, Plus, Trash2 } from 'lucide-react'
import { api, type KiloModelEntry, type KiloVirtualModel } from '../lib/api'

/**
 * 阶段 M：Kilo 命名虚拟模型管理。
 *
 * 自包含组件（不碰 KiloPage 现有 state）：列表 + 新建/编辑 + 删除 + 启停。
 * 候选从当前上游目录勾选（含付费档，用户已选择「Kilo 全部模型」）；
 * 策略三种，race 会放大上游调用与训练暴露，必须显式提示。
 */

type Draft = {
  name: string
  models: string[]
  strategy: KiloVirtualModel['strategy']
  enabled: boolean
}

const EMPTY_DRAFT: Draft = { name: '', models: [], strategy: 'fallback', enabled: true }

const STRATEGY_LABEL: Record<KiloVirtualModel['strategy'], string> = {
  fallback: '按序切换（默认）',
  round_robin: '轮询',
  race: '并发竞速（谁快用谁）',
}

const STRATEGY_HINT: Record<KiloVirtualModel['strategy'], string> = {
  fallback: '按候选顺序试，失败/限流自动换下一个；每次只打 1 个上游。',
  round_robin: '每次请求轮换起点，之后仍按序切换，分散负载。',
  race: '同时发全部候选，第一个成功者胜出。',
}

export default function VirtualModelsCard({
  models,
  toast,
}: {
  models: KiloModelEntry[]
  toast: (msg: string, ok?: boolean) => void
}) {
  const [list, setList] = useState<KiloVirtualModel[]>([])
  const [loading, setLoading] = useState(true)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState('')

  const reload = useCallback(async () => {
    try {
      const r = await api.kiloVirtualList()
      setList(r.models ?? [])
    } catch {
      /* 读不到不阻断 */
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void reload() }, [reload])

  const openNew = () => { setDraft({ ...EMPTY_DRAFT, models: [] }); setEditing(false) }
  const openEdit = (vm: KiloVirtualModel) => {
    setDraft({ name: vm.name, models: [...vm.models], strategy: vm.strategy, enabled: vm.enabled })
    setEditing(true)
  }

  const save = async () => {
    if (!draft) return
    if (!/^[a-zA-Z0-9_-]{1,48}$/.test(draft.name.trim())) {
      toast('名称只允许字母、数字、-、_（1~48 字符）', false)
      return
    }
    if (draft.models.length === 0) {
      toast('请至少勾选一个候选模型', false)
      return
    }
    setBusy(true)
    try {
      await api.kiloVirtualUpsert({
        name: draft.name.trim(), models: draft.models, strategy: draft.strategy, enabled: draft.enabled,
      })
      toast('虚拟模型已保存')
      setDraft(null)
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (name: string) => {
    if (!window.confirm(`删除虚拟模型「${name}」？`)) return
    setBusy(true)
    try {
      await api.kiloVirtualDelete(name)
      toast('已删除')
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (vm: KiloVirtualModel) => {
    setBusy(true)
    try {
      await api.kiloVirtualUpsert({ ...vm, enabled: !vm.enabled })
      void reload()
    } catch (e) {
      toast((e as Error).message, false)
    } finally {
      setBusy(false)
    }
  }

  const toggleCandidate = (id: string) => {
    if (!draft) return
    const has = draft.models.includes(id)
    setDraft({
      ...draft,
      models: has ? draft.models.filter((m) => m !== id) : [...draft.models, id],
    })
  }

  const copyName = async (name: string) => {
    try {
      await navigator.clipboard.writeText(`kilo/${name}`)
      toast(`已复制 kilo/${name}`)
    } catch {
      toast('复制失败', false)
    }
  }

  const visibleCandidates = models.filter(
    (m) => m.id.toLowerCase().includes(filter.toLowerCase()) || (m.provider_name ?? '').toLowerCase().includes(filter.toLowerCase()),
  )

  return (
    <div className="rounded-2xl border border-zinc-200 bg-white p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-[15px] font-semibold text-zinc-800">虚拟模型</div>
          <div className="mt-0.5 text-[11.5px] text-zinc-400">
            把一个名字绑到一组候选模型，客户端用 <code className="font-mono">kilo/名字</code> 调用；上游真实响应模型原样透传
          </div>
        </div>
        <button
          onClick={openNew}
          className="rounded-lg border border-zinc-200 px-2.5 py-1.5 text-[12.5px] text-zinc-600 hover:bg-zinc-50"
        >
          <Plus className="mr-1 inline h-3.5 w-3.5" />
          新建
        </button>
      </div>

      {/* 已有虚拟模型 */}
      <div className="mt-3 space-y-2">
        {loading ? (
          <div className="py-4 text-center text-zinc-400"><Loader2 className="mx-auto h-4 w-4 animate-spin" /></div>
        ) : list.length === 0 ? (
          <div className="rounded-xl border border-zinc-200 bg-zinc-50/60 px-3 py-4 text-center text-[12.5px] text-zinc-400">
            暂无虚拟模型，点「新建」创建
          </div>
        ) : (
          list.map((vm) => (
            <div key={vm.name} className="flex flex-wrap items-center gap-2 rounded-xl border border-zinc-200 px-3 py-2.5">
              <span className={clsx('rounded px-1.5 py-0.5 text-[11px]', vm.enabled ? 'bg-green-50 text-green-700' : 'bg-zinc-100 text-zinc-500')}>
                {vm.enabled ? '启用' : '停用'}
              </span>
              <code className="font-mono text-[13px] text-zinc-800">kilo/{vm.name}</code>
              <span className="rounded bg-indigo-50 px-1.5 py-0.5 text-[11px] text-indigo-700">
                {STRATEGY_LABEL[vm.strategy]}
              </span>
              <span className="text-[11.5px] text-zinc-500">{vm.models.length} 个候选</span>
              <div className="ml-auto flex items-center gap-1.5">
                <button onClick={() => void copyName(vm.name)} title="复制调用名" className="rounded border border-zinc-200 p-1 text-zinc-500 hover:text-zinc-800">
                  <Copy className="h-3.5 w-3.5" />
                </button>
                <button onClick={() => void toggle(vm)} disabled={busy} className="rounded border border-zinc-200 px-1.5 py-1 text-[11.5px] text-zinc-600 hover:bg-zinc-50 disabled:opacity-40">
                  {vm.enabled ? '停用' : '启用'}
                </button>
                <button onClick={() => openEdit(vm)} title="编辑" className="rounded border border-zinc-200 p-1 text-zinc-500 hover:text-zinc-800">
                  <Pencil className="h-3.5 w-3.5" />
                </button>
                <button onClick={() => void remove(vm.name)} disabled={busy} title="删除" className="rounded border border-rose-200 p-1 text-rose-600 hover:bg-rose-50 disabled:opacity-40">
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
              <div className="w-full truncate font-mono text-[11px] text-zinc-400" title={vm.models.join(', ')}>
                {vm.models.join(' · ')}
              </div>
            </div>
          ))
        )}
      </div>

      {/* 新建/编辑表单 */}
      {draft && (
        <div className="mt-3 rounded-xl border border-zinc-200 bg-zinc-50/60 p-3">
          <div className="text-[13px] font-semibold text-zinc-800">{editing ? '编辑' : '新建'}虚拟模型</div>
          <div className="mt-2 grid gap-2 sm:grid-cols-2">
            <label className="text-[12px] text-zinc-600">
              名称
              <input
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                disabled={editing}
                placeholder="fast-code"
                className="mt-1 w-full rounded-lg border border-zinc-200 px-2.5 py-1.5 font-mono text-[12.5px] focus:outline-none focus:ring-2 focus:ring-teal-500/30 disabled:bg-zinc-100"
              />
            </label>
            <label className="text-[12px] text-zinc-600">
              策略
              <select
                value={draft.strategy}
                onChange={(e) => setDraft({ ...draft, strategy: e.target.value as Draft['strategy'] })}
                className="mt-1 w-full rounded-lg border border-zinc-200 px-2.5 py-1.5 text-[12.5px] focus:outline-none"
              >
                {(Object.keys(STRATEGY_LABEL) as Draft['strategy'][]).map((s) => (
                  <option key={s} value={s}>{STRATEGY_LABEL[s]}</option>
                ))}
              </select>
            </label>
          </div>
          <div className="mt-1.5 text-[11.5px] text-zinc-500">{STRATEGY_HINT[draft.strategy]}</div>
          {draft.strategy === 'race' && (
            <div className="mt-1.5 rounded-lg border border-rose-200 bg-rose-50 px-2.5 py-1.5 text-[11.5px] text-rose-700">
              ⚠️ 竞速会把同一份提示词<b>同时发给全部 {draft.models.length || 'N'} 个候选</b>：Kilo 免费档声明内容可能被用于训练，
              若勾了付费档还会产生 N 倍计费。请确认可接受。
            </div>
          )}
          <label className="mt-2 flex items-center gap-1.5 text-[12px] text-zinc-600">
            <input type="checkbox" checked={draft.enabled} onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })} />
            启用
          </label>

          <div className="mt-2 flex items-center justify-between gap-2">
            <div className="text-[12px] font-medium text-zinc-700">
              候选模型（已选 {draft.models.length}）
            </div>
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="筛选…"
              className="w-40 rounded-lg border border-zinc-200 px-2 py-1 text-[12px] focus:outline-none"
            />
          </div>
          <div className="mt-1.5 max-h-[220px] overflow-y-auto rounded-lg border border-zinc-200 bg-white">
            {visibleCandidates.length === 0 ? (
              <div className="px-2.5 py-3 text-center text-[12px] text-zinc-400">目录为空（先点「刷新目录」）</div>
            ) : (
              visibleCandidates.map((m) => (
                <label key={m.id} className="flex cursor-pointer items-center gap-2 border-b border-zinc-100 px-2.5 py-1.5 last:border-0 hover:bg-zinc-50">
                  <input
                    type="checkbox"
                    checked={draft.models.includes(m.id)}
                    onChange={() => toggleCandidate(m.id)}
                  />
                  <span className="flex-1 truncate font-mono text-[11.5px] text-zinc-700">{m.id}</span>
                  <span className={clsx('rounded px-1 py-0.5 text-[10px]', m.free ? 'bg-green-50 text-green-700' : 'bg-amber-50 text-amber-700')}>
                    {m.free ? '免费' : '付费'}
                  </span>
                </label>
              ))
            )}
          </div>
          <div className="mt-1 text-[11px] text-zinc-400">
            候选含付费档时需先关掉「只显示免费模型」；每个候选仍受 free_only 边界约束。
          </div>

          <div className="mt-2 flex items-center gap-2">
            <button
              onClick={() => void save()}
              disabled={busy}
              className="rounded-lg bg-zinc-900 px-3 py-1.5 text-[12.5px] text-white hover:bg-zinc-800 disabled:opacity-40"
            >
              {busy ? <Loader2 className="mr-1 inline h-3.5 w-3.5 animate-spin" /> : null}
              保存
            </button>
            <button onClick={() => setDraft(null)} className="rounded-lg border border-zinc-200 px-3 py-1.5 text-[12.5px] text-zinc-600 hover:bg-zinc-100">
              取消
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
