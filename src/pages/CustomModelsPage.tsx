import { useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { Loader2, Pencil, Plus, PlugZap, Activity, Trash2, X, Plug, RefreshCw, ListChecks, Search } from 'lucide-react'
import { api, type CustomKeyStrategy, type CustomProviderInput, type CustomProviderTestResult, type CustomProviderView, type CustomProtocol, type PluginProviderView, type PluginStatus } from '../lib/api'

// 自定义模型源表单（新增/编辑共用）。编辑时 key 留空 = 保留原 key。
type FormState = {
  id: string
  name: string
  protocol: CustomProtocol
  base_url: string
  /** 多 key，一行一个（textarea 原文） */
  api_keys: string
  key_strategy: CustomKeyStrategy
  via_proxy: boolean
  enabled: boolean
  /** 全量模型清单（上游 ID；来自视图或测试结果） */
  allModels: string[]
  /** 全部暴露（默认 true；false = 只暴露 allowed 里的勾选项） */
  exposeAll: boolean
  /** 勾选暴露的模型 */
  allowed: Set<string>
  /** 编辑中的原条目 id（空 = 新增） */
  editing: string | null
}

const emptyForm = (): FormState => ({
  id: '',
  name: '',
  protocol: 'openai',
  base_url: '',
  api_keys: '',
  key_strategy: 'round_robin',
  via_proxy: false,
  enabled: true,
  allModels: [],
  exposeAll: true,
  allowed: new Set(),
  editing: null,
})

const PROTOCOLS: { value: CustomProtocol; label: string; hint: string }[] = [
  { value: 'openai', label: 'OpenAI 兼容', hint: 'https://api.openai.com/v1' },
  { value: 'anthropic', label: 'Anthropic', hint: 'https://api.anthropic.com/v1' },
  { value: 'responses', label: 'OpenAI Responses', hint: 'https://api.openai.com/v1' },
  { value: 'gemini', label: 'Google Gemini', hint: 'https://generativelanguage.googleapis.com/v1beta' },
]

/** id 规则与后端一致：字母数字开头，可含 - _，≤32 字符 */
const validId = (id: string) => /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$/.test(id)

// 插件式供应商：状态徽标（设计文档 docs/PLUGIN-PROVIDERS.md §六）
const STATUS_META: Record<PluginStatus, { label: string; cls: string }> = {
  running: { label: '运行中', cls: 'bg-emerald-50 text-emerald-700' },
  need_config: { label: '待配置', cls: 'bg-amber-50 text-amber-700' },
  disabled: { label: '已停用', cls: 'bg-zinc-200 text-zinc-500' },
  starting: { label: '启动中', cls: 'bg-sky-50 text-sky-700' },
  error: { label: '异常', cls: 'bg-red-50 text-red-600' },
}

// 插件式供应商：状态筛选下拉选项
const PLUGIN_FILTERS: { value: PluginStatus | 'all'; label: string }[] = [
  { value: 'all', label: '全部' },
  { value: 'running', label: '运行中' },
  { value: 'need_config', label: '待配置' },
  { value: 'disabled', label: '已停用' },
  { value: 'error', label: '异常' },
]

// 插件编辑弹层状态：id/路径只读；provider.json 全文编辑，保存前前端校验 JSON 合法性
type PluginEditState = {
  id: string
  name: string
  path: string
  /** provider.json 当前编辑内容 */
  content: string
  /** 打开弹层时的磁盘内容（「重置为磁盘内容」用） */
  disk: string
}

// 插件暴露模型弹层状态（对齐自定义源表单交互）
type PluginExposeState = {
  id: string
  name: string
  /** 全量模型清单（弹层勾选用；来自后端 models_all） */
  allModels: string[]
  /** 全部暴露（默认 true；false = 只暴露 allowed 里的勾选项） */
  exposeAll: boolean
  /** 暴露白名单 */
  allowed: Set<string>
}

// 顶层 name 编辑 → 同步写回 JSON 的 name 字段（id/entry 由后端保护，前端不动）
const applyJsonName = (json: string, name: string): string => {
  try {
    const obj = JSON.parse(json) as Record<string, unknown>
    obj.name = name
    return JSON.stringify(obj, null, 2)
  } catch {
    return json // 当前内容非法 JSON：仅记录 name 输入，保存时统一拒绝
  }
}

// 从 provider.json 读顶层 name（打开弹层/重置时回填显示名）
const jsonName = (json: string): string => {
  try {
    const obj = JSON.parse(json) as { name?: unknown }
    return typeof obj.name === 'string' ? obj.name : ''
  } catch {
    return ''
  }
}

export default function CustomModelsPage({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [list, setList] = useState<CustomProviderView[] | null>(null)
  const [form, setForm] = useState<FormState | null>(null)
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [testResult, setTestResult] = useState<CustomProviderTestResult | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [confirmClear, setConfirmClear] = useState(false)
  const [clearing, setClearing] = useState(false)

  // 双标签：默认展示自定义供应商 tab（插件列表仍在 useEffect 预加载，切过去即有数据）
  const [tab, setTab] = useState<'plugins' | 'custom'>('custom')
  const [plugins, setPlugins] = useState<PluginProviderView[] | null>(null)
  const [pluginFilter, setPluginFilter] = useState<PluginStatus | 'all'>('all')
  const [pluginEditing, setPluginEditing] = useState<PluginEditState | null>(null)
  const [pluginExposing, setPluginExposing] = useState<PluginExposeState | null>(null)
  const [pluginConfirmDelete, setPluginConfirmDelete] = useState<string | null>(null)
  const [pluginBusy, setPluginBusy] = useState(false)
  const [modelSearch, setModelSearch] = useState('')
  // 启停/保存后子进程状态异步推进（starting→running/need_config），延迟刷新让状态落定
  const pluginRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // toast 用 ref 封装（App 的 showToast 每次渲染重建），effect 只跑一次
  const toastRef = useRef(toast)
  toastRef.current = toast

  const reload = async () => {
    try {
      const r = await api.customProvidersList()
      setList(r.providers ?? [])
    } catch (e) {
      console.error('加载自定义模型源失败', e)
      toastRef.current('加载自定义模型源失败', false)
    }
  }

  useEffect(() => {
    void reload()
    void loadPlugins()
  }, [])

  useEffect(() => {
    return () => {
      if (pluginRefreshTimer.current) clearTimeout(pluginRefreshTimer.current)
    }
  }, [])

  const openAdd = () => {
    setTestResult(null)
    setForm(emptyForm())
  }

  const openEdit = (p: CustomProviderView) => {
    setTestResult(null)
    setForm({
      id: p.id,
      name: p.name,
      protocol: p.protocol,
      base_url: p.base_url,
      api_keys: (p.api_keys && p.api_keys.length > 0 ? p.api_keys : p.api_key ? [p.api_key] : []).join('\n'), // 回填已存 keys
      key_strategy: p.key_strategy ?? 'round_robin',
      via_proxy: p.via_proxy,
      enabled: p.enabled,
      allModels: p.models_all ?? [],
      exposeAll: !p.allowed_models || p.allowed_models.length === 0,
      allowed: new Set(p.allowed_models ?? []),
      editing: p.id,
    })
  }

  // 表单 → 请求项（id 编辑中不可改）。
  // forTest=true 时跳过源 ID 校验：测试只验证 base_url + key 连通性，源 ID 可后填
  // （后端测试接口对空 ID 自动占位 _test；保存时才要求 ID 合规——保存后不可改、作模型前缀）。
  const formToInput = (f: FormState, forTest = false): CustomProviderInput | null => {
    if (!forTest && !validId(f.id.trim())) {
      toast('源 ID 需字母数字开头，可含 - _，不超过 32 字符', false)
      return null
    }
    if (!/^https?:\/\/.+/.test(f.base_url.trim())) {
      toast('API 地址需为 http(s) URL', false)
      return null
    }
    const keys = f.api_keys.split('\n').map((k) => k.trim()).filter(Boolean)
    const allowed = f.exposeAll ? undefined : Array.from(f.allowed)
    if (!f.exposeAll && allowed && allowed.length === 0) {
      toast('请至少勾选一个要暴露的模型，或选择「全部暴露」')
      return null
    }
    return {
      id: f.id.trim() || (forTest ? '_test' : ''),
      name: f.name.trim() || f.id.trim() || (forTest ? '_test' : ''),
      protocol: f.protocol,
      base_url: f.base_url.trim(),
      api_keys: keys.length > 0 ? keys : undefined, // 整体留空 = 保留原 keys
      key_strategy: f.key_strategy,
      allowed_models: allowed,
      via_proxy: f.via_proxy,
      enabled: f.enabled,
    }
  }

  // 「测试并获取模型」：当前表单临时拉取目录（不落盘）。
  // forTest=true：未填源 ID 也允许测试（后端 _test 占位），只校验 API 地址。
  const doTest = async () => {
    if (!form) return
    const input = formToInput(form, true)
    if (!input) return
    setTesting(true)
    setTestResult(null)
    try {
      const r = await api.customProvidersTest(input)
      setTestResult(r)
      if (r.models && r.models.length > 0) {
        // 测试拿到最新全量清单：保留现有勾选状态（新模型默认不勾）。
        setForm((prev) => (prev ? { ...prev, allModels: r.models! } : prev))
      }
    } catch (e) {
      setTestResult({ ok: false, error: String(e) })
    } finally {
      setTesting(false)
    }
  }

  // 活性探测：手动触发（后台每 5 分钟也会自动刷新健康）
  const probing = useRef<string | null>(null)
  const doProbe = async (id: string) => {
    if (probing.current) return
    probing.current = id
    try {
      const r = await api.customProvidersProbe(id)
      toast(r.ok ? `探测成功 · ${r.latency_ms}ms` : `探测失败：${r.error}`, r.ok)
      await reload()
    } catch (e) {
      toast(`探测失败：${String(e)}`, false)
    } finally {
      probing.current = null
    }
  }

  // 清空全部自定义源（含本地模型缓存）；与设置页「数据清理」互不影响
  const doClearAll = async () => {
    setClearing(true)
    try {
      await api.customProvidersClear()
      setList([])
      setConfirmClear(false)
      toast('已清空全部自定义模型源', true)
    } catch (e) {
      toast(`清空失败：${String(e)}`, false)
    } finally {
      setClearing(false)
    }
  }

  /** 上次成功时间的短显示（HH:MM） */
  const fmtTime = (iso?: string) => {
    if (!iso) return ''
    try {
      const d = new Date(iso)
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    } catch {
      return ''
    }
  }

  // 保存：整表提交（现有列表去掉编辑项 + 表单项）
  const doSave = async () => {
    if (!form) return
    const input = formToInput(form)
    if (!input) return
    setSaving(true)
    try {
      const others = (list ?? [])
        .filter((p) => p.id !== form.editing)
        .map<CustomProviderInput>((p) => ({
          id: p.id,
          name: p.name,
          protocol: p.protocol,
          base_url: p.base_url,
          api_keys: p.api_keys && p.api_keys.length > 0 ? p.api_keys : undefined,
          key_strategy: p.key_strategy,
          via_proxy: p.via_proxy,
          enabled: p.enabled,
        }))
      const r = await api.customProvidersSave([...others, input])
      setList(r.providers ?? [])
      setForm(null)
      setModelSearch('')
      setTestResult(null)
      toast(`已保存：模型名前缀 ${input.id}/，立即生效`, true)
    } catch (e) {
      console.error('保存失败', e)
      toast(`保存失败：${String(e)}`, false)
    } finally {
      setSaving(false)
    }
  }

  // 删除 / 启停 / via_proxy 切换：整表保存（去掉或修改目标项）
  const saveAll = async (providers: CustomProviderInput[], okMsg: string) => {
    setSaving(true)
    try {
      const r = await api.customProvidersSave(providers)
      setList(r.providers ?? [])
      toast(okMsg, true)
    } catch (e) {
      console.error('保存失败', e)
      toast(`保存失败：${String(e)}`, false)
    } finally {
      setSaving(false)
    }
  }

  // 整表重建输入（启停/删除复用）：必须带上每个源的完整白名单，否则提交体缺
  // allowed_models 键会被后端「空=全部暴露」语义擦除（问题 4 数据丢失根因）。
  const toInputs = (ps: CustomProviderView[]): CustomProviderInput[] =>
    ps.map((p) => ({
      id: p.id,
      name: p.name,
      protocol: p.protocol,
      base_url: p.base_url,
      api_keys: p.api_keys && p.api_keys.length > 0 ? p.api_keys : undefined,
      key_strategy: p.key_strategy,
      via_proxy: p.via_proxy,
      enabled: p.enabled,
      // 带上当前白名单（空 = 该源本就是全部暴露，序列化后键省略、后端不误伤）
      allowed_models: p.allowed_models && p.allowed_models.length > 0 ? p.allowed_models : undefined,
    }))

  const doDelete = async (id: string) => {
    setConfirmDelete(null)
    await saveAll(toInputs((list ?? []).filter((p) => p.id !== id)), `已删除 ${id}`)
  }

  const toggleEnabled = async (p: CustomProviderView) => {
    await saveAll(
      toInputs((list ?? []).map((x) => (x.id === p.id ? { ...x, enabled: !x.enabled } : x))),
      p.enabled ? `已停用 ${p.id}（模型不再出现在 /v1/models）` : `已启用 ${p.id}`,
    )
  }

  // ── 插件式供应商（R4）：列表 / 重扫 / 启停 / 编辑保存 / 删除 ──

  const loadPlugins = async () => {
    try {
      const r = await api.pluginsList()
      setPlugins(r.plugins ?? [])
    } catch (e) {
      console.error('加载插件式供应商失败', e)
      toastRef.current('加载插件式供应商失败', false)
    }
  }

  // 切到插件 tab 时刷新一次（子进程状态后端异步变化，不进列表时保持落后）
  const switchTab = (t: 'plugins' | 'custom') => {
    setTab(t)
    if (t === 'plugins') void loadPlugins()
  }

  const schedulePluginRefresh = (ms = 2500) => {
    if (pluginRefreshTimer.current) clearTimeout(pluginRefreshTimer.current)
    pluginRefreshTimer.current = setTimeout(() => {
      void loadPlugins()
    }, ms)
  }

  const rescanPlugins = async () => {
    setPluginBusy(true)
    try {
      const r = await api.pluginsRescan()
      setPlugins(r.plugins ?? [])
      toast('已重新扫描 providers/ 目录', true)
    } catch (e) {
      toast(`重新扫描失败：${String(e)}`, false)
    } finally {
      setPluginBusy(false)
    }
  }

  // 启停：enabled=true 拉起子进程+注册厂商；false 停进程+注销（模型移出 /v1/models，不删文件）
  const doPluginToggle = async (p: PluginProviderView) => {
    const turningOn = p.status === 'disabled'
    setPluginBusy(true)
    try {
      const r = await api.pluginToggle(p.id, turningOn)
      setPlugins((prev) => prev?.map((x) => (x.id === p.id ? r.plugin : x)) ?? null)
      toast(
        turningOn ? `已启用 ${p.id}：子进程已拉起` : `已停用 ${p.id}（模型不再出现在 /v1/models）`,
        true,
      )
      if (turningOn) schedulePluginRefresh()
    } catch (e) {
      toast(`${turningOn ? '启用' : '停用'}失败：${String(e)}`, false)
    } finally {
      setPluginBusy(false)
    }
  }

  const openPluginEdit = (p: PluginProviderView) => {
    setPluginEditing({
      id: p.id,
      name: jsonName(p.provider_json) || p.id,
      path: p.path,
      content: p.provider_json,
      disk: p.provider_json,
    })
  }

  // 打开暴露模型弹层（对齐自定义源表单交互）
  const openPluginExpose = (p: PluginProviderView) => {
    setPluginExposing({
      id: p.id,
      name: p.name || p.id,
      allModels: p.models_all ?? [],
      exposeAll: p.expose_all,
      allowed: new Set(p.exposed_models ?? []),
    })
  }

  // 保存暴露白名单（主进程侧过滤，保存后聚合目录/网关即时生效）
  const savePluginExpose = async () => {
    if (!pluginExposing) return
    if (!pluginExposing.exposeAll && pluginExposing.allowed.size === 0) {
      toast('请至少勾选一个要暴露的模型，或选择「全部暴露」', false)
      return
    }
    setPluginBusy(true)
    try {
      const r = await api.pluginSaveExposedModels(
        pluginExposing.id,
        pluginExposing.exposeAll,
        pluginExposing.exposeAll ? [] : Array.from(pluginExposing.allowed),
      )
      setPlugins((prev) => prev?.map((x) => (x.id === pluginExposing.id ? r.plugin : x)) ?? null)
      setPluginExposing(null)
      setModelSearch('')
      toast(`已保存 ${pluginExposing.id} 的暴露模型（${r.plugin.expose_all ? '全部暴露' : `白名单 ${r.plugin.exposed_models?.length ?? 0} 个`}）`, true)
      schedulePluginRefresh()
    } catch (e) {
      toast(`保存失败：${String(e)}`, false)
    } finally {
      setPluginBusy(false)
    }
  }

  // 保存编辑：前端先校验 JSON 合法性；id/entry 及 provider_private_configs 内部由后端/供应商校验
  const savePlugin = async () => {
    if (!pluginEditing) return
    try {
      JSON.parse(pluginEditing.content)
    } catch {
      toast('provider.json 不是合法 JSON，已拒绝保存', false)
      return
    }
    setPluginBusy(true)
    try {
      const r = await api.pluginSaveConfig(pluginEditing.id, pluginEditing.content)
      setPlugins((prev) => prev?.map((x) => (x.id === pluginEditing.id ? r.plugin : x)) ?? null)
      setPluginEditing(null)
      toast(`已保存 ${pluginEditing.id} 的 provider.json`, true)
      schedulePluginRefresh()
    } catch (e) {
      toast(`保存失败：${String(e)}`, false)
    } finally {
      setPluginBusy(false)
    }
  }

  // 删除：停进程 + 整目录删除（providers/<id>/ 下 provider.json、exe、data/ 全部移除，不可恢复）
  const doPluginDelete = async (id: string) => {
    setPluginConfirmDelete(null)
    setPluginBusy(true)
    try {
      await api.pluginDelete(id)
      setPlugins((prev) => prev?.filter((x) => x.id !== id) ?? null)
      toast(`已删除 ${id}（providers/${id}/ 已移除）`, true)
    } catch (e) {
      toast(`删除失败：${String(e)}`, false)
    } finally {
      setPluginBusy(false)
    }
  }

  const visiblePlugins = (plugins ?? []).filter((p) => pluginFilter === 'all' || p.status === pluginFilter)

  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-[16px] font-semibold text-zinc-900 flex items-center gap-2.5">
            <Plug size={18} className="text-teal-700" />
            自定义模型
          </h1>
          <p className="text-zinc-500 text-xs mt-1">
            接入自带 API Key 的第三方模型供应商（OpenAI / Anthropic / Gemini 三种协议），可同时接入多个。
            保存后模型进入 /v1/models（模型名带 <code className="bg-zinc-100 px-1 rounded">源ID/</code> 前缀），调用、日志、统计与节点池全部复用统一网关。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {tab === 'plugins' ? (
            <button
              onClick={() => void rescanPlugins()}
              disabled={pluginBusy}
              className="flex items-center gap-1.5 border border-zinc-200 text-zinc-600 rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-50 whitespace-nowrap disabled:opacity-50"
              title="重新扫描 providers/ 目录"
            >
              <RefreshCw size={14} className={pluginBusy ? 'animate-spin' : ''} />
              重新扫描
            </button>
          ) : (
            <>
              {(list?.length ?? 0) > 0 && (
                <button
                  onClick={() => setConfirmClear(true)}
                  className="flex items-center gap-1.5 border border-red-200 text-red-600 rounded-lg px-3 py-1.5 text-[13px] hover:bg-red-50 whitespace-nowrap"
                >
                  <Trash2 size={14} />
                  清空全部
                </button>
              )}
              <button
                onClick={openAdd}
                className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-700 whitespace-nowrap"
              >
                <Plus size={14} />
                添加模型源
              </button>
            </>
          )}
        </div>
      </div>

      {/* 双标签（设计文档 §六）：用户自定义供应商在前、插件式供应商在后，默认展示自定义 tab */}
      <div className="flex items-center gap-4 border-b border-zinc-200">
        <button
          type="button"
          onClick={() => switchTab('custom')}
          className={clsx(
            'pb-2 -mb-px text-[13px] font-medium border-b-2 transition-colors',
            tab === 'custom' ? 'border-zinc-900 text-zinc-900' : 'border-transparent text-zinc-500 hover:text-zinc-700',
          )}
        >
          用户自定义供应商
        </button>
        <button
          type="button"
          onClick={() => switchTab('plugins')}
          className={clsx(
            'pb-2 -mb-px text-[13px] font-medium border-b-2 transition-colors',
            tab === 'plugins' ? 'border-zinc-900 text-zinc-900' : 'border-transparent text-zinc-500 hover:text-zinc-700',
          )}
        >
          插件式供应商
        </button>
      </div>

      {tab === 'custom' ? (
        <>
          {/* 清空全部二次确认 */}
      {confirmClear && (
        <div className="bg-red-50 border border-red-200 rounded-2xl p-4 flex items-center gap-3 text-sm">
          <span className="flex-1 text-red-700">
            确认清空全部 {list?.length ?? 0} 个自定义模型源？将同时删除本地模型清单缓存；内建模型源与其它数据不受影响（设置页「数据清理」也不会再动自定义模型源）。
          </span>
          <button
            type="button"
            onClick={() => void doClearAll()}
            disabled={clearing}
            className="px-3 py-1.5 rounded bg-red-600 text-white hover:bg-red-700 disabled:opacity-50 whitespace-nowrap"
          >
            {clearing ? '清空中…' : '确认清空'}
          </button>
          <button type="button" onClick={() => setConfirmClear(false)} className="px-3 py-1.5 rounded bg-white border border-zinc-200 text-zinc-600 whitespace-nowrap">
            取消
          </button>
        </div>
      )}

      {/* 源列表 */}
      {list === null ? (
        <div className="text-zinc-500">加载中...</div>
      ) : list.length === 0 ? (
        <div className="bg-white rounded-2xl border p-8 text-center text-zinc-500 text-sm">
          还没有自定义模型源。点击右上角「添加模型源」，填入供应商的 API 地址与 Key 即可接入。
        </div>
      ) : (
        <div className="space-y-3">
          {list.map((p) => (
            <div key={p.id} className={clsx('bg-white rounded-2xl border p-4', !p.enabled && 'opacity-60')}>
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0 space-y-1">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-[15px] font-medium text-zinc-900">{p.name || p.id}</span>
                    <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-100 text-zinc-600 font-mono">{p.protocol}</span>
                    {!p.enabled && <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-200 text-zinc-500">已停用</span>}
                    {p.via_proxy && <span className="text-[11px] px-1.5 py-0.5 rounded bg-amber-50 text-amber-700">走节点池</span>}
                    {p.keys_total > 1 && (
                      <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-100 text-zinc-600">
                        {p.keys_total} key{p.key_strategy === 'failover' ? ' · 错误转移' : ' · 轮询'}
                      </span>
                    )}
                  </div>
                  <div className="text-xs text-zinc-500 font-mono truncate">{p.base_url}</div>
                  <div className="text-xs text-zinc-500">
                    前缀 <code className="bg-zinc-100 px-1 rounded">{p.id}/</code> · {p.models} 个模型
                    {p.allowed_models && p.allowed_models.length > 0 && p.models_all && p.models_all.length > 0 ? `（共 ${p.models_all.length}，白名单）` : ''}
                    {p.api_key_set ? ` · ${p.api_keys?.length ?? p.keys_total} 个 Key` : ' · 无 Key'}
                    {p.keys_total > 1 && (
                      <>
                        {' '}（可用 {p.keys_available}
                        {p.keys_cooling > 0 && <span className="text-amber-600"> · 冷却 {p.keys_cooling}</span>}
                        {p.keys_disabled > 0 && <span className="text-red-500"> · 禁用 {p.keys_disabled}</span>}）
                      </>
                    )}
                    {p.last_error ? <span className="text-red-500"> · {p.last_error}</span> : null}
                  </div>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  {/* 活性徽标 + 手动探测 */}
                  <button
                    type="button"
                    onClick={() => void doProbe(p.id)}
                    className={clsx(
                      'flex items-center gap-1 px-2 py-1 rounded-lg text-[11px] font-medium transition-colors',
                      p.last_error ? 'bg-red-50 text-red-600 hover:bg-red-100' : p.last_success ? 'bg-emerald-50 text-emerald-700 hover:bg-emerald-100' : 'bg-zinc-100 text-zinc-500 hover:bg-zinc-200',
                    )}
                    title={p.last_error ? p.last_error : p.last_success ? `上次成功 ${p.last_success}` : '尚未探测'}
                  >
                    <Activity size={12} />
                    {p.last_error ? '异常' : p.last_success ? `活跃 ${fmtTime(p.last_success)}` : '探测'}
                  </button>
                  {/* 启停 */}
                  <button
                    type="button"
                    onClick={() => void toggleEnabled(p)}
                    disabled={saving}
                    className={clsx(
                      'relative inline-flex h-6 w-11 items-center rounded-full transition-colors disabled:opacity-50',
                      p.enabled ? 'bg-zinc-900' : 'bg-zinc-200',
                    )}
                    aria-label={p.enabled ? '停用' : '启用'}
                  >
                    <span
                      className={clsx(
                        'inline-block h-5 w-5 transform rounded-full bg-white border border-zinc-300 transition-transform',
                        p.enabled ? 'translate-x-[22px]' : 'translate-x-[2px]',
                      )}
                    />
                  </button>
                  <button
                    type="button"
                    onClick={() => openEdit(p)}
                    className="p-2 rounded-lg text-zinc-500 hover:bg-zinc-100"
                    aria-label="编辑"
                  >
                    <Pencil size={15} />
                  </button>
                  <button
                    type="button"
                    onClick={() => setConfirmDelete(p.id)}
                    className="p-2 rounded-lg text-red-500 hover:bg-red-50"
                    aria-label="删除"
                  >
                    <Trash2 size={15} />
                  </button>
                </div>
              </div>

              {/* 删除二次确认 */}
              {confirmDelete === p.id && (
                <div className="mt-3 flex items-center gap-2 text-xs bg-red-50 rounded-lg p-2.5">
                  <span className="flex-1 text-red-700">删除源 {p.id}？使用其前缀模型的客户端将不可用。</span>
                  <button type="button" onClick={() => void doDelete(p.id)} disabled={saving} className="px-2.5 py-1 rounded bg-red-600 text-white hover:bg-red-700 disabled:opacity-50">
                    删除
                  </button>
                  <button type="button" onClick={() => setConfirmDelete(null)} className="px-2.5 py-1 rounded bg-white border border-zinc-200 text-zinc-600">
                    取消
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* 新增/编辑弹层 */}
      {form && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-900/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-[722px] max-h-[90vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <div className="text-[15px] font-semibold text-zinc-900">{form.editing ? `编辑模型源 · ${form.editing}` : '添加模型源'}</div>
              <button type="button" onClick={() => { setForm(null); setModelSearch('') }} className="p-1.5 rounded-lg text-zinc-400 hover:bg-zinc-100">
                <X size={16} />
              </button>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">源 ID</label>
              <input
                type="text"
                placeholder="如 myglm / openrouter"
                value={form.id}
                disabled={!!form.editing}
                onChange={(e) => setForm({ ...form, id: e.target.value })}
                className="w-full px-3 py-2 border rounded-lg font-mono disabled:bg-zinc-50 disabled:text-zinc-400"
              />
              <p className="text-zinc-500 text-xs">字母数字开头，可含 - _；模型将以 <code className="bg-zinc-100 px-1 rounded">{form.id || '源ID'}/模型名</code> 形式出现在 /v1/models</p>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">显示名称</label>
              <input
                type="text"
                placeholder="如 智谱 GLM"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                className="w-full px-3 py-2 border rounded-lg"
              />
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">协议</label>
              <div className="grid grid-cols-2 gap-2">
                {PROTOCOLS.map((p) => (
                  <button
                    key={p.value}
                    type="button"
                    onClick={() => setForm({ ...form, protocol: p.value })}
                    className={clsx(
                      'px-3 py-2 rounded-lg border text-[13px] transition-colors',
                      form.protocol === p.value ? 'border-zinc-900 bg-zinc-900 text-white' : 'border-zinc-200 text-zinc-600 hover:bg-zinc-50',
                    )}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              <p className="text-zinc-500 text-xs">API 根地址示例：{PROTOCOLS.find((p) => p.value === form.protocol)?.hint}</p>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">API 地址（base_url）</label>
              <input
                type="text"
                placeholder={PROTOCOLS.find((p) => p.value === form.protocol)?.hint}
                value={form.base_url}
                onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                className="w-full px-3 py-2 border rounded-lg font-mono text-[13px]"
              />
              <p className="text-zinc-500 text-xs">填到版本根路径（含 /v1 或 /v1beta），不要带尾斜杠</p>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">API Key（一行一个，可多个）</label>
              <textarea
                rows={3}
                placeholder={'sk-xxx1\nsk-xxx2（本地无鉴权网关可留空）'}
                value={form.api_keys}
                onChange={(e) => setForm({ ...form, api_keys: e.target.value })}
                spellCheck={false}
                className="w-full px-3 py-2 border rounded-lg font-mono text-[13px] resize-y"
              />
              <p className="text-zinc-500 text-xs">
                多 key 自动负载均衡与故障切换；Key 保存在本机配置中由网关持有，调用方无需携带；编辑时留空 = 保留原有 keys
              </p>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">Key 调度策略</label>
              <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => setForm({ ...form, key_strategy: 'round_robin' })}
                  className={clsx(
                    'px-3 py-2 rounded-lg border text-[13px] transition-colors text-left',
                    form.key_strategy === 'round_robin' ? 'border-zinc-900 bg-zinc-900 text-white' : 'border-zinc-200 text-zinc-600 hover:bg-zinc-50',
                  )}
                >
                  轮询
                  <span className={clsx('block text-[11px] mt-0.5', form.key_strategy === 'round_robin' ? 'text-zinc-300' : 'text-zinc-400')}>
                    均匀分摊到各 key
                  </span>
                </button>
                <button
                  type="button"
                  onClick={() => setForm({ ...form, key_strategy: 'failover' })}
                  className={clsx(
                    'px-3 py-2 rounded-lg border text-[13px] transition-colors text-left',
                    form.key_strategy === 'failover' ? 'border-zinc-900 bg-zinc-900 text-white' : 'border-zinc-200 text-zinc-600 hover:bg-zinc-50',
                  )}
                >
                  错误转移
                  <span className={clsx('block text-[11px] mt-0.5', form.key_strategy === 'failover' ? 'text-zinc-300' : 'text-zinc-400')}>
                    主 key 优先，冷却/失效才降级
                  </span>
                </button>
              </div>
              <p className="text-zinc-500 text-xs">仅作用于本自定义源，与实例池的路由模式互不影响；429 冷却（Retry-After）、401/403 禁用后自动换 key</p>
            </div>

            <div className="flex items-center space-x-3">
              <label className="relative inline-flex items-center cursor-pointer">
                <input
                  type="checkbox"
                  checked={form.via_proxy}
                  onChange={(e) => setForm({ ...form, via_proxy: e.target.checked })}
                  className="sr-only peer"
                />
                <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
              </label>
              <span className="text-sm text-zinc-700">出站走节点池代理</span>
              <span className="text-zinc-500 text-xs">（默认直连；供应商有地区限制时开启）</span>
            </div>

            {/* 暴露模型白名单 */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label className="block text-sm font-medium text-zinc-700">暴露模型</label>
                <label className="flex items-center gap-1.5 text-xs text-zinc-600 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={form.exposeAll}
                    onChange={(e) => setForm({ ...form, exposeAll: e.target.checked })}
                    className="accent-zinc-900"
                  />
                  全部暴露
                </label>
              </div>
              {form.allModels.length === 0 ? (
                <p className="text-zinc-500 text-xs">先「测试并获取模型」拉取清单后可勾选要暴露的模型；留空默认全部暴露</p>
              ) : (
                <>
                  <div className="relative">
                    <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-400 pointer-events-none" />
                    <input
                      type="text"
                      placeholder="搜索模型…"
                      value={modelSearch}
                      onChange={(e) => setModelSearch(e.target.value)}
                      className={clsx('w-full border rounded-lg pl-8 pr-3 py-1.5 text-[13px] text-zinc-700 placeholder:text-zinc-400 focus:outline-none focus:ring-1 focus:ring-zinc-400', form.exposeAll && 'opacity-50 pointer-events-none')}
                    />
                  </div>
                  <div className={clsx('border rounded-lg max-h-44 overflow-y-auto', form.exposeAll && 'opacity-50 pointer-events-none')}>
                    {form.allModels
                      .filter((m) => !modelSearch || m.toLowerCase().includes(modelSearch.toLowerCase()))
                      .map((m) => (
                        <label key={m} className="flex items-center gap-2 px-3 py-1.5 text-[13px] font-mono text-zinc-700 hover:bg-zinc-50 cursor-pointer border-b last:border-b-0">
                          <input
                            type="checkbox"
                            checked={form.exposeAll || form.allowed.has(m)}
                            disabled={form.exposeAll}
                            onChange={() => {
                              setForm((prev) => {
                                if (!prev) return prev
                                const next = new Set(prev.allowed)
                                if (next.has(m)) next.delete(m)
                                else next.add(m)
                                return { ...prev, allowed: next }
                              })
                            }}
                            className="accent-zinc-900"
                          />
                          <span className="truncate">{m}</span>
                        </label>
                      ))}
                    {form.allModels.filter((m) => !modelSearch || m.toLowerCase().includes(modelSearch.toLowerCase())).length === 0 && (
                      <div className="px-3 py-2 text-xs text-zinc-400">无匹配模型</div>
                    )}
                  </div>
                  {!form.exposeAll && (
                    <div className="flex items-center gap-3 text-xs text-zinc-500">
                      <span>已勾选 {form.allowed.size} / {form.allModels.length}</span>
                      <button type="button" className="text-zinc-600 hover:text-zinc-900 underline" onClick={() => setForm({ ...form, allowed: new Set(form.allModels) })}>全选</button>
                      <button type="button" className="text-zinc-600 hover:text-zinc-900 underline" onClick={() => setForm({ ...form, allowed: new Set() })}>清零</button>
                    </div>
                  )}
                  <p className="text-zinc-500 text-xs">未勾选的模型不会出现在 /v1/models，也无法经网关调用</p>
                </>
              )}
            </div>

            {/* 测试结果 */}
            {testResult && (
              <div className={clsx('rounded-lg p-3 text-xs space-y-1.5', testResult.ok ? 'bg-emerald-50 text-emerald-800' : 'bg-red-50 text-red-700')}>
                <div className="font-medium">{testResult.ok ? '全部 key 连通成功' : '部分/全部 key 连通失败'}</div>
                <div className="space-y-1">
                  {(testResult.results ?? []).map((kr, i) => (
                    <div key={i} className={clsx('flex items-center gap-2 font-mono', kr.ok ? 'text-emerald-700' : 'text-red-600')}>
                      <span className="w-20 shrink-0">{kr.key_tail || '(无key)'}</span>
                      <span>{kr.ok ? `✓ ${kr.count} 模型 · ${kr.latency_ms}ms` : `✗ ${kr.error}`}</span>
                    </div>
                  ))}
                  {!testResult.results?.length && !testResult.ok && <div>{testResult.error}</div>}
                </div>
              </div>
            )}

            <div className="flex items-center gap-2 pt-1">
              <button
                type="button"
                onClick={() => void doTest()}
                disabled={testing || saving}
                className="flex items-center gap-1.5 px-4 py-2 rounded-lg border border-zinc-300 text-[13px] text-zinc-700 hover:bg-zinc-50 disabled:opacity-60 disabled:cursor-not-allowed"
              >
                {testing ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
                {testing ? '测试中…' : '测试并获取模型'}
              </button>
              <div className="flex-1" />
              <button
                type="button"
                onClick={() => void doSave()}
                disabled={testing || saving}
                className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:opacity-60 disabled:cursor-not-allowed"
              >
                {saving ? <Loader2 size={14} className="animate-spin" /> : null}
                {saving ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
        </>
      ) : (
        <div className="space-y-3">
          {/* 顶部帮助文案 + 状态筛选下拉（设计文档 §六） */}
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-zinc-500 flex-1 min-w-0">
              将供应商文件夹放入安装目录 <code className="bg-zinc-100 px-1 rounded">providers/</code> 下（含 exe + provider.json），点「重新扫描」即可接入。
            </p>
            <select
              value={pluginFilter}
              onChange={(e) => setPluginFilter(e.target.value as PluginStatus | 'all')}
              className="px-2.5 py-1.5 rounded-lg border border-zinc-200 bg-white text-[12px] text-zinc-600 outline-none shrink-0"
              title="按状态筛选"
            >
              {PLUGIN_FILTERS.map((f) => (
                <option key={f.value} value={f.value}>
                  {f.label}
                </option>
              ))}
            </select>
          </div>

          {/* 插件列表 */}
          {plugins === null ? (
            <div className="text-zinc-500">加载中...</div>
          ) : plugins.length === 0 ? (
            <div className="bg-white rounded-2xl border p-8 text-center text-zinc-500 text-sm">
              还没有插件式供应商。将供应商文件夹（含可执行文件 + provider.json）放入安装目录 providers/ 下，点右上角「重新扫描」即可接入。
            </div>
          ) : visiblePlugins.length === 0 ? (
            <div className="bg-white rounded-2xl border p-8 text-center text-zinc-500 text-sm">
              没有匹配「{PLUGIN_FILTERS.find((f) => f.value === pluginFilter)?.label ?? ''}」状态的插件。
            </div>
          ) : (
            <div className="space-y-3">
              {visiblePlugins.map((p) => (
                <div key={p.id} className={clsx('bg-white rounded-2xl border p-4', p.status === 'disabled' && 'opacity-60')}>
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0 space-y-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-[15px] font-medium text-zinc-900">{p.name || p.id}</span>
                        <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-100 text-zinc-600 font-mono">{p.id}</span>
                        <span className={clsx('text-[11px] px-1.5 py-0.5 rounded', STATUS_META[p.status]?.cls ?? 'bg-zinc-100 text-zinc-600')}>
                          {STATUS_META[p.status]?.label ?? p.status}
                        </span>
                        <span className="text-[11px] px-1.5 py-0.5 rounded bg-zinc-100 text-zinc-600">v{p.version}</span>
                      </div>
                      <div className="text-xs text-zinc-500 font-mono truncate" title={p.path}>
                        {p.path}
                      </div>
                      <div className="text-xs text-zinc-500">
                        {p.models} 个模型
                        {!p.expose_all && p.exposed_models ? ` · 白名单 ${p.exposed_models.length} 个` : ''}
                        {p.started_at ? ` · 启动 ${fmtTime(p.started_at)}` : ''}
                        {p.restart_count > 0 ? ` · 已重启 ${p.restart_count} 次` : ''}
                        {p.last_error ? <span className="text-red-500"> · {p.last_error}</span> : null}
                      </div>
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      {/* 启停（与自定义源同款 switch；disabled 状态 = 已停用） */}
                      <button
                        type="button"
                        onClick={() => void doPluginToggle(p)}
                        disabled={pluginBusy}
                        className={clsx(
                          'relative inline-flex h-6 w-11 items-center rounded-full transition-colors disabled:opacity-50',
                          p.status !== 'disabled' ? 'bg-zinc-900' : 'bg-zinc-200',
                        )}
                        aria-label={p.status === 'disabled' ? '启用' : '停用'}
                      >
                        <span
                          className={clsx(
                            'inline-block h-5 w-5 transform rounded-full bg-white border border-zinc-300 transition-transform',
                            p.status !== 'disabled' ? 'translate-x-[22px]' : 'translate-x-[2px]',
                          )}
                        />
                      </button>
                      <button
                        type="button"
                        onClick={() => openPluginExpose(p)}
                        disabled={p.status !== 'running' || (p.models_all ?? []).length === 0}
                        className="p-2 rounded-lg text-zinc-500 hover:bg-zinc-100 disabled:opacity-40 disabled:cursor-not-allowed"
                        aria-label="暴露模型"
                        title={p.status !== 'running' ? '插件运行中才能配置暴露模型' : (p.models_all ?? []).length === 0 ? '尚未获取到模型清单' : '自定义要暴露给 /v1/models 的模型'}
                      >
                        <ListChecks size={15} />
                      </button>
                      <button
                        type="button"
                        onClick={() => openPluginEdit(p)}
                        className="p-2 rounded-lg text-zinc-500 hover:bg-zinc-100"
                        aria-label="编辑"
                      >
                        <Pencil size={15} />
                      </button>
                      <button
                        type="button"
                        onClick={() => setPluginConfirmDelete(p.id)}
                        className="p-2 rounded-lg text-red-500 hover:bg-red-50"
                        aria-label="删除"
                      >
                        <Trash2 size={15} />
                      </button>
                    </div>
                  </div>

                  {/* 删除二次确认（明确告知停进程 + 整目录删除不可恢复） */}
                  {pluginConfirmDelete === p.id && (
                    <div className="mt-3 flex items-center gap-2 text-xs bg-red-50 rounded-lg p-2.5">
                      <span className="flex-1 text-red-700">
                        删除插件 {p.id}？将停止其进程，并删除整个 <code className="bg-red-100 px-1 rounded">providers/{p.id}/</code> 目录（provider.json、exe 及 data/ 下数据），此操作不可恢复。
                      </span>
                      <button
                        type="button"
                        onClick={() => void doPluginDelete(p.id)}
                        disabled={pluginBusy}
                        className="px-2.5 py-1 rounded bg-red-600 text-white hover:bg-red-700 disabled:opacity-50"
                      >
                        删除
                      </button>
                      <button type="button" onClick={() => setPluginConfirmDelete(null)} className="px-2.5 py-1 rounded bg-white border border-zinc-200 text-zinc-600">
                        取消
                      </button>
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* 插件式供应商「暴露模型」弹层（对齐自定义源表单交互） */}
      {pluginExposing && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-900/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-[722px] max-h-[90vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <div className="text-[15px] font-semibold text-zinc-900">暴露模型 · {pluginExposing.name}</div>
              <button type="button" onClick={() => { setPluginExposing(null); setModelSearch('') }} className="p-1.5 rounded-lg text-zinc-400 hover:bg-zinc-100">
                <X size={16} />
              </button>
            </div>

            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label className="block text-sm font-medium text-zinc-700">暴露模型</label>
                <label className="flex items-center gap-1.5 text-xs text-zinc-600 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={pluginExposing.exposeAll}
                    onChange={(e) => setPluginExposing((prev) => (prev ? { ...prev, exposeAll: e.target.checked } : prev))}
                    className="accent-zinc-900"
                  />
                  全部暴露
                </label>
              </div>
              <div className="relative">
                <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-400 pointer-events-none" />
                <input
                  type="text"
                  placeholder="搜索模型…"
                  value={modelSearch}
                  onChange={(e) => setModelSearch(e.target.value)}
                  className={clsx('w-full border rounded-lg pl-8 pr-3 py-1.5 text-[13px] text-zinc-700 placeholder:text-zinc-400 focus:outline-none focus:ring-1 focus:ring-zinc-400', pluginExposing.exposeAll && 'opacity-50 pointer-events-none')}
                />
              </div>
              <div className={clsx('border rounded-lg max-h-72 overflow-y-auto', pluginExposing.exposeAll && 'opacity-50 pointer-events-none')}>
                {pluginExposing.allModels
                  .filter((m) => !modelSearch || m.toLowerCase().includes(modelSearch.toLowerCase()))
                  .map((m) => (
                    <label key={m} className="flex items-center gap-2 px-3 py-1.5 text-[13px] font-mono text-zinc-700 hover:bg-zinc-50 cursor-pointer border-b last:border-b-0">
                      <input
                        type="checkbox"
                        checked={pluginExposing.exposeAll || pluginExposing.allowed.has(m)}
                        disabled={pluginExposing.exposeAll}
                        onChange={() => {
                          setPluginExposing((prev) => {
                            if (!prev) return prev
                            const next = new Set(prev.allowed)
                            if (next.has(m)) next.delete(m)
                            else next.add(m)
                            return { ...prev, allowed: next }
                          })
                        }}
                        className="accent-zinc-900"
                      />
                      <span className="truncate">{m}</span>
                    </label>
                  ))}
                {pluginExposing.allModels.filter((m) => !modelSearch || m.toLowerCase().includes(modelSearch.toLowerCase())).length === 0 && (
                  <div className="px-3 py-2 text-xs text-zinc-400">无匹配模型</div>
                )}
              </div>
              {!pluginExposing.exposeAll && (
                <div className="flex items-center gap-3 text-xs text-zinc-500">
                  <span>已勾选 {pluginExposing.allowed.size} / {pluginExposing.allModels.length}</span>
                  <button type="button" className="text-zinc-600 hover:text-zinc-900 underline" onClick={() => setPluginExposing((prev) => (prev ? { ...prev, allowed: new Set(prev.allModels) } : prev))}>全选</button>
                  <button type="button" className="text-zinc-600 hover:text-zinc-900 underline" onClick={() => setPluginExposing((prev) => (prev ? { ...prev, allowed: new Set() } : prev))}>清零</button>
                </div>
              )}
              <p className="text-zinc-500 text-xs">未勾选的模型不会出现在 /v1/models，也无法经网关调用</p>
            </div>

            <div className="flex items-center gap-2 pt-1">
              <div className="flex-1" />
              <button
                type="button"
                onClick={() => void savePluginExpose()}
                disabled={pluginBusy}
                className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:opacity-60 disabled:cursor-not-allowed"
              >
                {pluginBusy ? <Loader2 size={14} className="animate-spin" /> : null}
                {pluginBusy ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* 插件式供应商编辑弹层（provider.json 全文 JSON 编辑；id/路径只读） */}
      {pluginEditing && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-900/40 p-4">
          <div className="bg-white rounded-2xl shadow-xl w-full max-w-[722px] max-h-[90vh] overflow-y-auto p-6 space-y-4" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <div className="text-[15px] font-semibold text-zinc-900">编辑插件 · {pluginEditing.id}</div>
              <button type="button" onClick={() => setPluginEditing(null)} className="p-1.5 rounded-lg text-zinc-400 hover:bg-zinc-100">
                <X size={16} />
              </button>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">显示名称</label>
              <input
                type="text"
                value={pluginEditing.name}
                onChange={(e) =>
                  setPluginEditing((prev) =>
                    prev ? { ...prev, name: e.target.value, content: applyJsonName(prev.content, e.target.value) } : prev,
                  )
                }
                className="w-full px-3 py-2 border rounded-lg"
              />
              <p className="text-zinc-500 text-xs">写入 provider.json 顶层 name 字段</p>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">插件 ID（只读）</label>
              <input
                type="text"
                value={pluginEditing.id}
                disabled
                readOnly
                className="w-full px-3 py-2 border rounded-lg font-mono disabled:bg-zinc-50 disabled:text-zinc-400"
              />
              <p className="text-zinc-500 text-xs">由目录名决定；模型以 <code className="bg-zinc-100 px-1 rounded">{pluginEditing.id}/模型名</code> 形式出现在 /v1/models</p>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">路径（只读）</label>
              <div className="w-full px-3 py-2 border rounded-lg font-mono text-[12px] text-zinc-500 bg-zinc-50 truncate" title={pluginEditing.path}>
                {pluginEditing.path}
              </div>
            </div>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-zinc-700">provider.json（全文 JSON 编辑）</label>
              <textarea
                rows={14}
                value={pluginEditing.content}
                onChange={(e) => setPluginEditing((prev) => (prev ? { ...prev, content: e.target.value } : prev))}
                spellCheck={false}
                className="w-full px-3 py-2 border rounded-lg font-mono text-[12px] resize-y leading-relaxed"
              />
              <p className="text-zinc-500 text-xs">
                顶层 <code className="bg-zinc-100 px-1 rounded">id</code> / <code className="bg-zinc-100 px-1 rounded">entry</code> 由系统管理，请勿修改；provider_private_configs 内部结构由供应商自行校验。
              </p>
            </div>

            <div className="flex items-center gap-2 pt-1">
              <button
                type="button"
                onClick={() =>
                  setPluginEditing((prev) => (prev ? { ...prev, content: prev.disk, name: jsonName(prev.disk) } : prev))
                }
                className="px-4 py-2 rounded-lg border border-zinc-300 text-[13px] text-zinc-700 hover:bg-zinc-50"
                title="丢弃本次编辑，重新加载磁盘上的 provider.json"
              >
                重置为磁盘内容
              </button>
              <div className="flex-1" />
              <button
                type="button"
                onClick={() => void savePlugin()}
                disabled={pluginBusy}
                className="flex items-center gap-1.5 bg-zinc-900 text-white rounded-lg px-4 py-2 text-[13px] hover:bg-zinc-700 disabled:opacity-60 disabled:cursor-not-allowed"
              >
                {pluginBusy ? <Loader2 size={14} className="animate-spin" /> : null}
                {pluginBusy ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
