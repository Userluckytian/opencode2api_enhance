// API 对接层：桌面(壳)与 Web 共用，统一调用 core 的 /api/admin/* HTTP 接口。
// 开机自启由 Go core 承载（写 Windows 注册表），经 HTTP 调用，与其它接口一致。
// 注意：窗口内容由 core 的 HTTP 服务提供（127.0.0.1:<port>），非 Tauri webview 环境，invoke 不可用。

// ─── 类型定义（与 Rust 端 serde 结构一一对应） ───────────────────────

export type InstanceStatus =
  | 'Stopped'
  | 'Starting'
  | 'Running'
  | 'Stopping'
  | { Error: string }

export type Instance = {
  name: string
  port: number
  node: string
  password: string
  ip: string
  singbox_port: number
  pid: number | null
  singbox_pid: number | null
  /** 是否加入统一网关池（默认 false = 独享实例） */
  join_gateway: boolean
  status: InstanceStatus
}

export type NodeView = {
  name: string
  node_type: string
  server: string
  port: number
  has_cred: boolean
  group: string
}

export type TestResult = {
  name: string
  port: number
  ok: boolean
  status_code: number | null
  model_count: number | null
  message: string
  latency_ms: number
}

export type BatchAddItem = {
  node: string
  name?: string | null
  port?: number | null
}

export type BatchAddResult = {
  added: { name: string; port: number; node: string }[]
  errors: { node: string; error: string }[]
  added_count: number
  error_count: number
}

export type BatchOpResult = {
  success: string[]
  errors: Record<string, string>
  success_count: number
  error_count: number
  /** S2: 批量启动跳过的已运行实例 */
  skipped?: string[]
  skipped_count?: number
}

export type PortCheckResult = {
  available: boolean
  reason: string
}

// 自定义模型源（第七页「自定义模型」）
export type CustomProtocol = 'openai' | 'anthropic' | 'gemini' | 'responses'

export type CustomKeyStrategy = 'round_robin' | 'failover'

export type CustomProviderView = {
  id: string
  name: string
  protocol: CustomProtocol
  base_url: string
  /** 全部 key 明文（面板已鉴权；key 本就明文存于本机配置）：编辑表单回填用 */
  api_keys: string[]
  /** 首 key（旧版兼容） */
  api_key: string
  /** 是否已配置 key */
  api_key_set: boolean
  /** key 调度策略：round_robin 轮询 | failover 错误转移（仅作用于本源） */
  key_strategy: CustomKeyStrategy
  via_proxy: boolean
  enabled: boolean
  /** 聚合目录中该源模型数（实时，经白名单过滤） */
  models: number
  /** 全量模型清单（上游 ID，编辑勾选用） */
  models_all: string[]
  /** 暴露白名单（空 = 全部暴露） */
  allowed_models: string[]
  /** 最近一次成功（探测/请求，RFC3339） */
  last_success?: string
  /** key 健康计数（运行时快照；无活实例时全 0） */
  keys_total: number
  keys_available: number
  keys_cooling: number
  keys_disabled: number
  last_error?: string
}

export type CustomProviderInput = {
  id: string
  name?: string
  protocol: CustomProtocol
  base_url: string
  /** 多 key（一行一个）；编辑时整体留空 = 保留原 keys */
  api_keys?: string[]
  /** 暴露白名单（空 = 全部暴露） */
  allowed_models?: string[]
  /** 单 key 兼容输入 */
  api_key?: string
  key_strategy?: CustomKeyStrategy
  via_proxy?: boolean
  enabled?: boolean
}

export type CustomKeyTestResult = {
  key_tail: string
  ok: boolean
  count?: number
  latency_ms: number
  error?: string
}

export type CustomProviderTestResult = {
  ok: boolean
  /** 逐 key 结果（多 key 一键全验） */
  results?: CustomKeyTestResult[]
  /** 首个成功 key 的模型清单（勾选界面刷新全量用） */
  models?: string[]
  count?: number
  latency_ms?: number
  error?: string
}

export type CustomProbeResult = {
  id: string
  ok: boolean
  latency_ms: number
  error?: string
  last_success?: string
}

// 插件式供应商（R1 后端 core/manager/pluginprovider.View，设计文档 docs/PLUGIN-PROVIDERS.md 七）
export type PluginStatus = 'running' | 'need_config' | 'disabled' | 'starting' | 'error'

export type PluginProviderView = {
  id: string
  name: string
  version: string
  status: PluginStatus
  /** 就绪后拉取的模型数（失败=0） */
  models: number
  /** 全量模型 ID 清单（暴露勾选弹层用；running 且拉取成功才有值） */
  models_all?: string[]
  /** 全部暴露（true 时 exposed_models 无意义） */
  expose_all: boolean
  /** 暴露白名单（expose_all=false 时生效） */
  exposed_models?: string[]
  /** providers/<id> 绝对路径（展示用） */
  path: string
  /** provider.json 全文（编辑回填） */
  provider_json: string
  pid?: number
  url?: string
  last_error?: string
  /** 最近就绪时间（RFC3339） */
  started_at?: string
  restart_count: number
}

export type PluginListResponse = {
  plugins: PluginProviderView[]
}

export type PluginSaveResponse = {
  status: string
  plugin: PluginProviderView
}

export type PluginToggleResponse = {
  status: string
  plugin: PluginProviderView
}

export type PluginDeleteResponse = {
  status: string
  deleted: string
}

export type ScanStatus = 'idle' | 'running' | 'stopping' | 'done' | 'error'

export type ProbeResult = {
  node: string
  node_type: string
  server: string
  port: number
  ok: boolean
  category: string
  status_code: number | null
  model_count: number | null
  message: string
  latency_ms: number
}

export type ScanProgress = {
  status: ScanStatus
  total: number
  current: number
  current_node: string | null
  results: ProbeResult[]
  error: string | null
  api_port: number
  socks_port: number
  started_ms: number | null
  finished_ms: number | null
  /** V1: 停止扫描统计（stop-scan 悬浮窗进度）：停止时活跃探针数 / 已中断探针对数 */
  stopping_count?: number
  stopped_count?: number
}

export type ConfigView = {
  base_url: string
  default_password: string
  has_password: boolean
  clash_external_url: string
  has_clash_token: boolean
  /** E1: 上游代理出口（socks5:// 或 http:// 前缀，留空 = 直连） */
  upstream_proxy: string
  timeout_ttft_min_ms: number
  timeout_ttft_max_ms: number
  timeout_silence_min_ms: number
  timeout_silence_max_ms: number
  failover_probe_min: number
  failover_probe_max: number
  call_log_max: number
  show_node_prefix: boolean
  /** U3: 界面轮询间隔（秒，0 = 关闭轮询，默认 5） */
  ui_poll_interval_sec: number
  subscribe_url: string
  subscribe_interval_min: number
  health_check_interval_sec: number
  health_restart_threshold: number
  has_gateway_key: boolean
  gateway_key: string
  /** 统一网关监听端口（0 = 未设置，用环境槽位/默认 40080） */
  gateway_port: number
  /** 实例池链路探活（P1） */
  pool_probe_interval_sec: number
  pool_probe_timeout_sec: number
  pool_quality_window_min: number
  pool_probe_enabled: boolean
  probe_solo_enabled: boolean
  /** 性能模式熔断/半开（P2） */
  pool_breaker_threshold: number
  pool_halfopen_interval_sec: number
  pool_performance_mode: boolean
  /** 请求级竞速（P2b） */
  pool_race_copies: number
  /** 并发设置（D3） */
  scan_concurrency: number
  /** N2: 停止扫描并发（默认 4） */
  stop_scan_concurrency: number
  batch_concurrency: number
  test_concurrency: number
  pool_probe_concurrency: number
}

export type BinariesInfo = {
  bin_dir: string
  oc_exists: boolean
  sb_exists: boolean
}

// ─── 全流程调用日志 ─────────────────────────────────────────────

export type CallLogEvent = {
  type: string
  node?: string
  detail?: string
  at?: string
}

export type CallLogRecord = {
  req_id: string
  ts: string
  path?: string
  model?: string
  stream?: boolean
  route_mode?: string
  nodes?: string[]
  events?: CallLogEvent[]
  status: string
  prompt_tokens?: number
  completion_tokens?: number
  duration_ms?: number
  err_msg?: string
  /** 来源标注：空 = 统一网关；否则为独享实例名（S4 聚合读取） */
  source?: string
}

// ─── 统一网关（实例池） ─────────────────────────────────────────────

export type GatewayStatus = {
  running: boolean
  address: string
  port: number
  api_key: string
  running_instances: number
  total_instances: number
  message: string
  route_mode: 'smart' | 'failover' | 'round_robin'
  free_models: string[]
  free_models_updated_at: number | null
  free_models_loading: boolean
  free_models_error: string | null
}

export type RestartPoolResult = {
  stopped: number
  started: number
  freed_ports: number[]
  gateway_running: boolean
  error: string | null
}

// ─── auto 虚拟模型（实例池设置弹层） ─────────────────────────────────────────────

/** auto 模型配置：enabled 开启后 /v1/models 顶部出现 auto；权重按模型展示名（0~10，0=永不参与，缺省 5） */
export type AutoModelConfig = {
  enabled: boolean
  /** 选择策略：balanced（默认，平滑加权轮询）/ speed（权重≥5 中选最快）/ quality（按权重锁定） */
  strategy?: string
  weights?: Record<string, number>
  /** 模型展示名 → 上下文上限 token（留空 = 保守默认 128k） */
  context_windows?: Record<string, number>
}

// ─── 实例池链路质量（性能模式 P1/P2） ─────────────────────────────────────────────

export type PoolQualityLevel = 'healthy' | 'degraded' | 'flaky' | 'down'

export type PoolQualityRecord = {
  name: string
  port: number
  singbox_port: number
  /** 质量分 0~100（未探测 = 100） */
  score: number
  level: PoolQualityLevel
  success_rate: number
  avg_latency_ms: number
  consecutive_failures: number
  last_probe_ts: number
  /** 最新一次失败原因（探测透传，悬停显示） */
  last_error?: string
}

export type PoolQualitySummary = {
  total: number
  probed: number
  healthy: number
  degraded: number
  flaky: number
  down: number
  last_scan_ts: number
  records: PoolQualityRecord[]
}

// ─── 残留进程探测与清除（孤儿实例 / 探针残留） ─────────────────────────────────────────────

export type OrphanProcess = {
  pid: number
  name: string
  /** probe = 探针扫描残留；orphan = 已停止实例残留 */
  category: 'probe' | 'orphan'
  instance?: string
  port?: number
  detail: string
}

export type OrphanScan = {
  total: number
  probe: number
  orphan: number
  items: OrphanProcess[]
}

export type OrphanKillResult = {
  killed: number[]
  errors: Record<string, string>
}

// ─── Token 统计（按实例） ─────────────────────────────────────────────

export type ModelStat = {
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}

export type GatewayNodeStat = {
  name: string
  addr: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}

export type InstanceStat = {
  name: string
  /** 实例目录存在但实例列表中已无（已删除/历史实例）时为 false */
  exists: boolean
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  models: ModelStat[]
  /** 仅统一网关条目：按节点（SOCKS5 出口）拆分的调用统计 */
  nodes?: GatewayNodeStat[]
}

export type StatsSummary = {
  total_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_tokens: number
  instances: InstanceStat[]
}

export type ResetStatsResult = {
  /** 成功重置的项数（含实例与统一网关） */
  reset_count: number
  /** 清除的「已删除实例」历史统计目录数 */
  deleted_count: number
  /** 失败明细 */
  failed: string[]
}

/** 单日统计（统计页按天查看：按统一网关调用日志聚合） */
export type DayStats = {
  day: string
  total_requests: number
  ok_requests: number
  fail_requests: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_tokens: number
  by_model: ModelStat[]
  by_node: GatewayNodeStat[]
}

// ─── 订阅（main 功能迁移 M1） ─────────────────────────────────────────────

export type SubscribeNode = {
  name: string
  server: string
  port: number
  node_type: string
  password?: string
  uuid?: string
  cipher?: string
  sni?: string
  network?: string
  ws_path?: string
  flow?: string
  tls: boolean
  raw: string
}

export type SubscribeResult = {
  nodes: SubscribeNode[]
  count: number
}

// T3: 订阅源（多条列表：URL / 自动拉取间隔 / 导入目标）
export type SubscriptionSource = {
  url: string
  interval_min: number
  target: 'solo' | 'pool' | 'pool-only'
  group?: string // 订阅分组名（导入时解析写入；手动指定后固定）
  name_pinned?: boolean // true = 用户手动指定分组名（自动拉取不覆盖）
}

export type SubscriptionTargetLabel = 'solo' | 'pool' | 'pool-only'

// ─── 健康巡检（main 功能迁移 M2） ─────────────────────────────────────────────

export type HealthRecord = {
  name: string
  healthy: boolean
  last_check_ts: number
  consecutive_failures: number
  last_error?: string
}

export type HealthSummary = {
  total: number
  healthy: number
  unhealthy: number
  records: HealthRecord[]
  last_scan_ts: number
}

// ─── 日志过滤与聚合（main 功能迁移 M4） ─────────────────────────────────────────────

export type CallLogFilter = {
  node?: string
  keyword?: string
  status?: 'ok' | 'error'
  limit?: number
  offset?: number
  from_ts?: string
  to_ts?: string
}

export type CallLogAggregate = {
  instance: string
  total: number
  errors: number
  last_ts: string
}

// ─── HTTP 对接层（core /api/admin/*） ─────────────────────────────

/** 后端基础地址：Web 同源为空；桌面壳可经构建注入 VITE_API_BASE。 */
const API_BASE: string = (import.meta.env?.VITE_API_BASE as string | undefined) ?? ''

async function req<T>(method: string, path: string, body?: unknown, qs?: Record<string, unknown>): Promise<T> {
  let url = API_BASE + '/api/admin' + path
  if (qs) {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(qs)) {
      if (v !== undefined && v !== null) p.set(k, String(v))
    }
    const s = p.toString()
    if (s) url += '?' + s
  }
  const opts: RequestInit = { method, headers: {} }
  if (body !== undefined) {
    opts.headers = { 'Content-Type': 'application/json' }
    opts.body = JSON.stringify(body)
  }
  const res = await fetch(url, opts)
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const j = await res.json()
      if (j && j.error) msg = j.error
    } catch { /* ignore */ }
    throw new Error(msg)
  }
  const ct = res.headers.get('content-type') ?? ''
  if (ct.includes('json')) return (await res.json()) as T
  if (ct.includes('text/html') || res.redirected) {
    // 后端返回登录页/重定向（开启鉴权但前端无登录页）或错误页：不把 HTML 当数据。
    throw new Error('需要登录或会话已过期，请刷新页面重试')
  }
  return (await res.text()) as unknown as T
}

export const api = {
  // 节点
  listNodes: () => req<NodeView[]>('GET', '/nodes'),
  /** 删除订阅缓存节点（外部 Clash 节点只读跳过；main 功能 M5） */
  deleteNode: (name: string) => req<{ removed: number }>('POST', '/nodes/delete', { name }),
  deleteNodes: (names: string[]) => req<{ removed: number }>('POST', '/nodes/delete-batch', { names }),

  // 实例
  listInstances: () => req<Instance[]>('GET', '/instances'),
  /** 手动刷新指定实例的状态（返回这些实例的最新状态） */
  refreshStates: (names: string[]) => req<Instance[]>('POST', '/instances/refresh', { names }),
  addInstance: (name: string, port: number, node: string, password: string) =>
    req<Instance>('POST', '/instances/add', { name, port, node, password }),
  removeInstance: (name: string) => req<void>('POST', '/instances/remove', { name }),
  startInstance: (name: string) => req<void>('POST', '/instances/start', { name }),
  stopInstance: (name: string) => req<void>('POST', '/instances/stop', { name }),
  testInstance: (name: string) => req<TestResult>('POST', '/instances/test', { name }),
  batchAdd: (nodes: BatchAddItem[], basePort?: number, useNodeName?: boolean, namePrefix?: string) =>
    req<BatchAddResult>('POST', '/instances/batch/add', {
      nodes,
      basePort: basePort ?? undefined,
      useNodeName: useNodeName ?? undefined,
      namePrefix: namePrefix ?? undefined,
    }),
  batchStart: (names: string[]) => req<BatchOpResult>('POST', '/instances/batch/start', { names }),
  batchStop: (names: string[]) => req<BatchOpResult>('POST', '/instances/batch/stop', { names }),
  batchDelete: (names: string[]) => req<BatchOpResult>('POST', '/instances/batch/delete', { names }),

  // 端口
  portSuggest: () => req<number>('GET', '/port/suggest'),
  portCheck: (port: number) => req<PortCheckResult>('GET', '/port/check', undefined, { port }),

  // 扫描
  scanStart: (opts?: {
    nodes?: string[]
    apiPort?: number
    socksPort?: number
    timeout?: number
    /** 并发 worker 数（可选，默认后端 8） */
    concurrency?: number
  }) =>
    req<ScanProgress>('POST', '/scan/start', {
      nodes: opts?.nodes ?? undefined,
      apiPort: opts?.apiPort ?? undefined,
      socksPort: opts?.socksPort ?? undefined,
      timeout: opts?.timeout ?? undefined,
      concurrency: opts?.concurrency ?? undefined,
    }),
  scanStatus: () => req<ScanProgress>('GET', '/scan/status'),
  scanStop: () => req<ScanProgress>('POST', '/scan/stop'),

  // 配置
  configGet: () => req<ConfigView>('GET', '/config'),
  configSet: (key: string, value: string) => req<void>('POST', '/config/set', { key, value }),
  // auto 虚拟模型：GET 当前配置 / POST 保存即传播子进程（热生效，无需重启）
  autoModelGet: () => req<AutoModelConfig>('GET', '/auto-model'),
  autoModelSave: (cfg: AutoModelConfig) => req<{ status: string; config: AutoModelConfig }>('POST', '/auto-model', cfg),

  // 自定义模型源（第七页「自定义模型」）：用户自带 key 接入第三方供应商
  customProvidersList: () => req<{ providers: CustomProviderView[] }>('GET', '/custom-providers'),
  /** 整表保存（增/改/删一次到位）；编辑时 api_key 留空 = 保留原 key */
  customProvidersSave: (providers: CustomProviderInput[]) =>
    req<{ status: string; providers: CustomProviderView[] }>('POST', '/custom-providers/save', { providers }),
  /** 连通测试（不落盘）：拉取模型目录，返回模型列表与延迟 */
  customProvidersTest: (input: CustomProviderInput) =>
    req<CustomProviderTestResult>('POST', '/custom-providers/test', input),
  /** 活性探测：真实拉一次上游目录，刷新健康并返回结果 */
  customProvidersProbe: (id: string) =>
    req<CustomProbeResult>('POST', '/custom-providers/probe', { id }),
  /** 清空全部自定义源（含目录磁盘缓存）；内建源与其它数据不受影响 */
  customProvidersClear: () =>
    req<{ status: string; cleared: boolean; providers: CustomProviderView[] }>('POST', '/custom-providers/clear'),

  // 插件式供应商（第七页「自定义模型」插件 tab）：providers/ 目录发现 / 重扫 / 配置保存 / 启停 / 删除
  pluginsList: () => req<PluginListResponse>('GET', '/plugins'),
  pluginsRescan: () => req<PluginListResponse>('POST', '/plugins/rescan'),
  pluginSaveConfig: (id: string, providerJSON: string) =>
    req<PluginSaveResponse>('POST', `/plugins/${encodeURIComponent(id)}/config`, { provider_json: providerJSON }),
  pluginToggle: (id: string, enabled: boolean) =>
    req<PluginToggleResponse>('POST', `/plugins/${encodeURIComponent(id)}/toggle`, { enabled }),
  pluginSaveExposedModels: (id: string, exposeAll: boolean, exposedModels: string[]) =>
    req<PluginSaveResponse>('POST', `/plugins/${encodeURIComponent(id)}/exposed-models`, {
      expose_all: exposeAll,
      exposed_models: exposedModels,
    }),
  pluginDelete: (id: string) => req<PluginDeleteResponse>('DELETE', `/plugins/${encodeURIComponent(id)}`),

  // 订阅（main 功能 M1）：preview 拉取解析、import 建实例、import-pool 仅入缓存
  subscribePreview: (url: string) => req<SubscribeResult>('POST', '/subscribe/preview', { url }),
  subscribeImport: (url: string, joinGateway?: boolean) =>
    req<{ status: string; imported: number }>('POST', '/subscribe/import', { url, join_gateway: joinGateway ?? undefined }),
  subscribeImportPool: (url: string) =>
    req<{ status: string; imported: number }>('POST', '/subscribe/import-pool', { url }),

  // T3: 订阅源列表管理（多条订阅：新增/删除/立即拉取 + 列表）
  subscriptionsList: () => req<{ subscriptions: SubscriptionSource[] }>('GET', '/subscriptions'),
  subscriptionsCount: (url: string) =>
    req<{ group: string; running: number; stopped: number }>('GET', `/subscriptions/count?url=${encodeURIComponent(url)}`),
  // T3/P3: 新增订阅 = 保存源 + 立即导入节点池；返回 imported/target（拉取失败时 error 非空、
  // 源已保存）。target 可选，默认 'pool-only'（2026-08-16 决策：订阅导入一律只进节点池）。
  // name 可选：手动指定分组名（固定，自动拉取不覆盖）。
  subscriptionsAdd: (url: string, intervalMin: number, target?: SubscriptionTargetLabel, name?: string) =>
    req<{ status: string; imported: number; target: string; error?: string }>('POST', '/subscriptions/add', {
      url,
      interval_min: intervalMin,
      target: target ?? 'pool-only',
      name: name ?? undefined,
    }),
  subscriptionsDelete: (url: string) =>
    req<{ status: string; removed: boolean; group: string; running: number; stopped: number; released?: number; removed_nodes?: number; instances?: string[] }>(
      'POST',
      '/subscriptions/delete',
      { url },
    ),
  // name 可选：非空则先固定该源分组名再立即拉取（仅导入节点池）。
  subscriptionsImport: (url: string, name?: string) =>
    req<{ status: string; imported: number; target: string }>('POST', '/subscriptions/import', {
      url,
      name: name ?? undefined,
    }),

  // 开机自启：由 Go core 承载（写 Windows 注册表），经 HTTP 调用
  autostartGet: async (): Promise<boolean> => {
    const r = await req<{ enabled: boolean }>('GET', '/autostart')
    return r.enabled
  },
  autostartSet: (enabled: boolean) => req<void>('POST', '/autostart/set', { enabled }),

  // 二进制信息
  getBinariesInfo: () => req<BinariesInfo>('GET', '/binaries'),

  // Token 统计（按实例）
  getStats: () => req<StatsSummary>('GET', '/stats'),
  /** 按天统计（date=YYYY-MM-DD，空=全量；按统一网关 + 独享实例调用日志聚合） */
  statsByDay: (date?: string) => req<DayStats>('GET', '/stats/by-day', undefined, { date: date ?? undefined }),
  /** 重置全部 Token 统计（clearDeleted=同时清除已删除节点历史统计） */
  resetStats: (clearDeleted?: boolean) =>
    req<ResetStatsResult>('POST', '/stats/reset', undefined, { clearDeleted: clearDeleted ?? undefined }),

  // 全流程调用日志
  getCallLog: (limit?: number) =>
    req<CallLogRecord[]>('GET', '/call-log', undefined, { limit: limit ?? undefined }),
  /** 清空全部调用日志 */
  clearCallLog: () => req<void>('POST', '/call-log/clear'),
  /** 过滤查询日志（main 功能 M4） */
  callLogFiltered: (filter: CallLogFilter) => req<CallLogRecord[]>('POST', '/call-log/filtered', filter),
  /** 按节点组合聚合日志（main 功能 M4） */
  callLogAggregate: () => req<CallLogAggregate[]>('GET', '/call-log/aggregate'),

  // 健康巡检（main 功能 M2）
  healthCheck: () => req<HealthSummary>('POST', '/health/check'),
  healthSummary: () => req<HealthSummary>('GET', '/health/summary'),

  // 统一网关（实例池）
  gatewayStatus: () => req<GatewayStatus>('GET', '/gateway/status'),
  gatewaySetRouteMode: (mode: 'smart' | 'failover' | 'round_robin') =>
    req<void>('POST', '/gateway/route-mode', { mode }),
  gatewayStop: () => req<void>('POST', '/gateway/stop'),
  setJoinGateway: (name: string, join: boolean) =>
    req<void>('POST', '/instances/join-gateway', { name, join }),

  // 一键重启实例池（全停→强制清端口→全启→网关同步）
  restartPool: () => req<RestartPoolResult>('POST', '/pool/restart'),

  // 实例池链路质量（性能模式 P1）：汇总视图 + 手动触发一轮探活
  poolQuality: () => req<PoolQualitySummary>('GET', '/pool/quality'),
  poolQualityProbe: () => req<PoolQualitySummary>('POST', '/pool/quality/probe'),

  // 残留进程：探测占着进程但未使用的节点/实例/探针 + 按 PID 一键清除
  orphanScan: () => req<OrphanScan>('GET', '/processes/orphans'),
  orphanKill: (pids: number[]) => req<OrphanKillResult>('POST', '/processes/orphans/kill', { pids }),

  // 清除数据（1=运行数据, 2=+实例记录, 3=全部重置）
  dataClean: (level: 1 | 2 | 3) => req<void>('POST', '/data/clean', { level }),
}
