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
}

export type PortCheckResult = {
  available: boolean
  reason: string
}

export type ScanStatus = 'idle' | 'running' | 'stopping' | 'done' | 'error'

export type SubscribeNode = {
  name: string
  server: string
  port: number
  node_type: string
  password?: string | null
  uuid?: string | null
  cipher?: string | null
  sni?: string | null
  network?: string | null
  ws_path?: string | null
  flow?: string | null
  tls: boolean
  raw: string
}

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
}

export type ConfigView = {
  base_url: string
  default_password: string
  has_password: boolean
  clash_external_url: string
  has_clash_token: boolean
  timeout_ttft_min_ms: number
  timeout_ttft_max_ms: number
  timeout_silence_min_ms: number
  timeout_silence_max_ms: number
  failover_probe_min: number
  failover_probe_max: number
  call_log_max: number
  show_node_prefix: boolean
  /** 统一网关监听端口（配置化，0 表示回退默认） */
  gateway_port: number
  /** 网关 API 密钥（已配置时返回 "***" 掩码） */
  gateway_key: string
  has_gateway_key: boolean
  /** 管理器自身 HTTP 服务端口（headless/桌面内嵌共用，0 表示回退默认） */
  http_port: number
  subscribe_url: string
  /** 订阅自动拉取间隔（分钟），0 = 不自动拉取 */
  subscribe_interval_min: number
  /** 健康巡检间隔（秒），0 = 关闭巡检 */
  health_check_interval_sec: number
  /** 连续失败达到该次数则自动重启实例 */
  health_restart_threshold: number
  /** 调用日志过滤关键词（逗号分隔，空 = 不过滤） */
  log_filter_keywords: string
}

export type BinariesInfo = {
  bin_dir: string
  oc_exists: boolean
  sb_exists: boolean
  /** 当前平台："windows" / "linux" / "macos"（前端据此显示子程序文件名） */
  platform: string
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
}

export type CallLogFilter = {
  node?: string
  keyword?: string
  status?: string
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

// ─── 健康巡检 ─────────────────────────────────────────────

export type HealthRecord = {
  name: string
  healthy: boolean
  last_check_ts: number
  consecutive_failures: number
  last_error?: string | null
}

export type HealthSummary = {
  total: number
  healthy: number
  unhealthy: number
  records: HealthRecord[]
  last_scan_ts: number
}

// ─── HTTP 基座（桌面与 headless 共用） ─────────────────────────────
//
// headless：前端与后端同源（都由 127.0.0.1:<http_port> 托管），相对路径即可。
// 桌面：WebView 经 Tauri custom-protocol（tauri://localhost）加载内置资源，
//      相对路径会解析到自定义协议而非后端，必须用绝对地址访问本地 HTTP 服务。
//      后端已配置 CorsLayer::permissive()，允许跨协议取数。
//      http_port 可配置（默认 19090），桌面前端经 get_http_port 动态获取，
//      避免配置改动后写死的端口失联。

const isTauri =
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window

let httpPort: number | null = null
const httpPortPromise = isTauri
  ? (async () => {
      try {
        const { invoke } = await import('@tauri-apps/api/core')
        httpPort = (await invoke<number>('get_http_port')) || 19090
      } catch {
        httpPort = 19090
      }
      return httpPort
    })()
  : Promise.resolve(null)

const API_ORIGIN = () => (isTauri ? `http://127.0.0.1:${httpPort ?? 19090}` : '')
const BASE = () => `${API_ORIGIN()}/api`

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  await httpPortPromise
  const res = await fetch(`${BASE()}${path}`, {
    headers: { 'Content-Type': 'application/json', ...(options?.headers ?? {}) },
    ...options,
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  const ct = res.headers.get('content-type') ?? ''
  if (ct.includes('application/json')) return (await res.json()) as T
  return (await res.text()) as unknown as T
}

const http = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) =>
    request<T>(path, {
      method: 'POST',
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
  del: <T>(path: string, body?: unknown) =>
    request<T>(path, {
      method: 'DELETE',
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
}

export type ResetStatsResult = {
  /** 成功重置的项数（含实例与统一网关） */
  reset_count: number
  /** 清除的「已删除实例」历史统计目录数 */
  deleted_count: number
  /** 失败明细 */
  failed: string[]
}

// ─── Tauri command 封装（经本地 HTTP API，桌面/headless 同源） ─────────────

export const api = {
  // 节点
  listNodes: () => http.get<NodeView[]>('/nodes'),
  deleteNode: (name: string) => http.post<{ removed: number }>('/nodes/delete', { name }).then((r) => r.removed),
  deleteNodes: (names: string[]) => http.post<{ removed: number }>('/nodes/delete-batch', { names }).then((r) => r.removed),

  // 实例
  listInstances: () => http.get<Instance[]>('/instances'),
  /** 手动刷新指定实例的状态（返回这些实例的最新状态） */
  refreshStates: (names: string[]) => http.get<Instance[]>(`/instances?refresh=${encodeURIComponent(JSON.stringify(names))}`),
  addInstance: (name: string, port: number, node: string, password: string) =>
    http.post<Instance>('/instances', { name, port, node, password }),
  removeInstance: (name: string) => http.post<void>(`/instances/${encodeURIComponent(name)}/remove`),
  startInstance: (name: string) => http.post<void>(`/instances/${encodeURIComponent(name)}`),
  stopInstance: (name: string) => http.post<void>(`/instances/${encodeURIComponent(name)}/stop`),
  testInstance: (name: string) => http.post<TestResult>(`/instances/${encodeURIComponent(name)}/test`),
  batchAdd: (nodes: BatchAddItem[], basePort?: number, useNodeName?: boolean, namePrefix?: string) =>
    http.post<BatchAddResult>('/instances/batch', {
      nodes,
      basePort: basePort ?? null,
      useNodeName: useNodeName ?? null,
      namePrefix: namePrefix ?? null,
    }),
  batchStart: (names: string[]) => http.post<BatchOpResult>('/instances/batch/start', { names }),
  batchStop: (names: string[]) => http.post<BatchOpResult>('/instances/batch/stop', { names }),
  batchDelete: (names: string[]) => http.del<BatchOpResult>('/instances/batch', { names }),

  // 端口
  portSuggest: () => http.get<{ port: number }>('/port/suggest').then((r) => r.port),
  portCheck: (port: number) => http.get<PortCheckResult>(`/port/check?port=${port}`),

  // 扫描
  scanStart: (opts?: {
    nodes?: string[]
    apiPort?: number
    socksPort?: number
    timeout?: number
    /** 并发 worker 数（可选，默认后端 8） */
    concurrency?: number
  }) =>
    http.post<ScanProgress>('/scan/start', {
      nodes: opts?.nodes ?? null,
      apiPort: opts?.apiPort ?? null,
      socksPort: opts?.socksPort ?? null,
      timeout: opts?.timeout ?? null,
      concurrency: opts?.concurrency ?? null,
    }),
  scanStatus: () => http.get<ScanProgress>('/scan/status'),
  scanStop: () => http.post<ScanProgress>('/scan/stop'),

  // 订阅拉取
  subscribePreview: (url: string) => http.post<SubscribeNode[]>('/subscribe/preview', { url }),
  subscribeImport: (url: string, joinGateway?: boolean) =>
    http.post<{ imported: number }>('/subscribe/import', { url, join_gateway: joinGateway ?? false }).then((r) => r.imported),
  subscribePoolImport: (url: string) =>
    http.post<{ imported: number }>('/subscribe/import-pool', { url }).then((r) => r.imported),

  // 配置
  configGet: () => http.get<ConfigView>('/config'),
  configSet: (key: string, value: string) => http.post<void>(`/config/${encodeURIComponent(key)}`, { value }),

  // 开机自启
  autostartGet: () => http.get<{ enabled: boolean }>('/autostart').then((r) => r.enabled),
  autostartSet: (enabled: boolean) => http.post<void>('/autostart', { enabled }),

  // 二进制信息
  getBinariesInfo: () => http.get<BinariesInfo>('/binaries'),

  // Token 统计（按实例）
  getStats: () => http.get<StatsSummary>('/stats'),
  /** 重置全部 Token 统计（clearDeleted=同时清除已删除节点历史统计） */
  resetStats: (clearDeleted?: boolean) =>
    http.post<ResetStatsResult>('/stats/reset', { clear_deleted: clearDeleted ?? null }),

  // 健康巡检
  healthCheck: () => http.post<HealthSummary>('/health/check'),
  healthSummary: () => http.get<HealthSummary>('/health/summary'),

  // 全流程调用日志
  getCallLog: (limit?: number) =>
    http.get<CallLogRecord[]>(limit === undefined ? '/call-log' : `/call-log?limit=${limit}`),
  callLogFiltered: (filter: CallLogFilter) =>
    http.post<CallLogRecord[]>('/call-log/filtered', filter),
  callLogAggregate: () => http.get<CallLogAggregate[]>('/call-log/aggregate'),
  /** 清空全部调用日志 */
  clearCallLog: () => http.post<void>('/call-log/clear'),

  // 报表导出
  exportCallLogCsv: async (limit?: number) => {
    await httpPortPromise
    const res = await fetch(`${BASE()}${'/export/call-log.csv'}${limit === undefined ? '' : `?limit=${limit}`}`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.text()
  },
  exportInstancesJson: async () => {
    await httpPortPromise
    const res = await fetch(`${BASE()}/export/instances.json`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.text()
  },
  exportStatsJson: async () => {
    await httpPortPromise
    const res = await fetch(`${BASE()}/export/stats.json`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return res.text()
  },

  // 统一网关（实例池）
  gatewayStatus: () => http.get<GatewayStatus>('/gateway'),
  gatewaySetRouteMode: (mode: 'smart' | 'failover' | 'round_robin') => http.post<void>('/gateway/route-mode', { mode }),
  gatewayStop: () => http.post<void>('/gateway/stop'),
  setJoinGateway: (name: string, join: boolean) => http.post<void>('/join-gateway', { name, join }),

  // 一键重启实例池（全停→强制清端口→全启→网关同步）
  restartPool: () => http.post<RestartPoolResult>('/instances/restart-pool'),

  // 清除数据（1=运行数据, 2=+实例记录, 3=全部重置）
  dataClean: (level: 1 | 2 | 3) => http.post<void>('/data-clean', { level }),
}

/** 把文本内容以附件形式下载到本地（报表导出共用） */
export function downloadText(filename: string, text: string) {
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
