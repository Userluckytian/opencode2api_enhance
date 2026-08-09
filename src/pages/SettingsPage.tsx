import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { api } from '../lib/api'
import type { ConfigView, BinariesInfo } from '../lib/api'

export default function SettingsPage({ toast }: { toast: (msg: string, ok?: boolean) => void }) {
  const [config, setConfig] = useState<ConfigView | null>(null)
  const [autostart, setAutostart] = useState<boolean>(false)
  const [binariesInfo, setBinariesInfo] = useState<BinariesInfo | null>(null)

  // Clash 外部控制表单
  const [clashUrl, setClashUrl] = useState('')
  const [clashToken, setClashToken] = useState('')

  // 网关超时切换区间表单
  const [timeoutForm, setTimeoutForm] = useState({
    timeout_ttft_min_ms: 10000,
    timeout_ttft_max_ms: 10000,
    timeout_silence_min_ms: 5000,
    timeout_silence_max_ms: 5000,
    failover_probe_min: 2,
    failover_probe_max: 3,
    call_log_max: 5000,
  })
  // 节点前缀展示开关（默认关闭）
  const [showNodePrefix, setShowNodePrefix] = useState(false)

  // 网关与端口表单
  const [gatewayPort, setGatewayPort] = useState('')
  const [gatewayKey, setGatewayKey] = useState('')
  const [httpPort, setHttpPort] = useState('')

  // 订阅表单
  const [subscribeUrl, setSubscribeUrl] = useState('')
  const [subscribeIntervalMin, setSubscribeIntervalMin] = useState(0)
  // 一键拉取目标：独享 / 进池
  const [subscribeTarget, setSubscribeTarget] = useState<'solo' | 'pool'>('solo')
  const [subscribeBusy, setSubscribeBusy] = useState(false)

  // 健康巡检表单
  const [healthCheckIntervalSec, setHealthCheckIntervalSec] = useState(0)
  const [healthRestartThreshold, setHealthRestartThreshold] = useState(3)

  // 日志过滤关键词
  const [logFilterKeywords, setLogFilterKeywords] = useState('')


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
        setClashUrl(cfg.clash_external_url)
        setTimeoutForm({
          timeout_ttft_min_ms: cfg.timeout_ttft_min_ms,
          timeout_ttft_max_ms: cfg.timeout_ttft_max_ms,
          timeout_silence_min_ms: cfg.timeout_silence_min_ms,
          timeout_silence_max_ms: cfg.timeout_silence_max_ms,
          failover_probe_min: cfg.failover_probe_min,
          failover_probe_max: cfg.failover_probe_max,
          call_log_max: cfg.call_log_max,
        })
        setShowNodePrefix(cfg.show_node_prefix)
        setGatewayPort(String(cfg.gateway_port))
        setHttpPort(String(cfg.http_port))
        setSubscribeUrl(cfg.subscribe_url)
        setSubscribeIntervalMin(cfg.subscribe_interval_min)
        setHealthCheckIntervalSec(cfg.health_check_interval_sec)
        setHealthRestartThreshold(cfg.health_restart_threshold)
        setLogFilterKeywords(cfg.log_filter_keywords)
      } catch (e) {
        console.error('加载设置失败', e)
        toast('加载设置失败', false)
      }
    }
    loadData()
  }, [toast])

  const handleSaveClash = async () => {
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
    }
  }

  const handleAutostartChange = async (enabled: boolean) => {
    try {
      await api.autostartSet(enabled)
      setAutostart(enabled)
      toast(enabled ? '已启用开机自启' : '已禁用开机自启', true)
    } catch (e) {
      console.error('设置开机自启失败', e)
      toast('设置失败', false)
    }
  }

  // 校验区间：min <= max，且为正数
  const validateRange = (min: number, max: number): boolean => {
    return min > 0 && max >= min
  }

  const handleSaveTimeout = async () => {
    const f = timeoutForm
    if (!validateRange(f.timeout_ttft_min_ms, f.timeout_ttft_max_ms) ||
        !validateRange(f.timeout_silence_min_ms, f.timeout_silence_max_ms) ||
        !validateRange(f.failover_probe_min, f.failover_probe_max)) {
      toast('区间不合法：最小值需 >0 且 最小值 ≤ 最大值', false)
      return
    }
    if (f.call_log_max < 100) {
      toast('日志保留上限至少 100 条', false)
      return
    }
    try {
      await api.configSet('timeout_ttft_min_ms', String(f.timeout_ttft_min_ms))
      await api.configSet('timeout_ttft_max_ms', String(f.timeout_ttft_max_ms))
      await api.configSet('timeout_silence_min_ms', String(f.timeout_silence_min_ms))
      await api.configSet('timeout_silence_max_ms', String(f.timeout_silence_max_ms))
      await api.configSet('failover_probe_min', String(f.failover_probe_min))
      await api.configSet('failover_probe_max', String(f.failover_probe_max))
      await api.configSet('call_log_max', String(f.call_log_max))
      toast('超时配置已保存（重启网关后生效）', true)
    } catch (e) {
      console.error('保存超时配置失败', e)
      toast('保存失败', false)
    }
  }

  const handleShowNodePrefixChange = async (enabled: boolean) => {
    try {
      await api.configSet('show_node_prefix', String(enabled))
      setShowNodePrefix(enabled)
      toast(enabled ? '已开启节点前缀展示' : '已关闭节点前缀展示', true)
    } catch (e) {
      console.error('设置节点前缀失败', e)
      toast('设置失败', false)
    }
  }

  const handleSaveGateway = async () => {
    try {
      if (gatewayPort.trim() && (Number(gatewayPort) < 1 || Number(gatewayPort) > 65535)) {
        toast('网关端口需在 1-65535 之间', false)
        return
      }
      await api.configSet('gateway_port', gatewayPort.trim())
      if (gatewayKey.trim()) {
        await api.configSet('gateway_key', gatewayKey.trim())
      }
      if (httpPort.trim() && (Number(httpPort) < 1 || Number(httpPort) > 65535)) {
        toast('HTTP 端口需在 1-65535 之间', false)
        return
      }
      await api.configSet('http_port', httpPort.trim())
      const cfg = await api.configGet()
      setConfig(cfg)
      setGatewayPort(String(cfg.gateway_port))
      setHttpPort(String(cfg.http_port))
      setGatewayKey('')
      toast('网关配置已保存并生效', true)
    } catch (e) {
      console.error('保存网关配置失败', e)
      toast('保存失败', false)
    }
  }

  const handleResetGatewayKey = async () => {
    if (!window.confirm('确定要重置网关密钥为默认值吗？已配置此密钥的上游客户端需同步更新。')) return
    try {
      await api.configSet('gateway_key', '')
      const cfg = await api.configGet()
      setConfig(cfg)
      toast('网关密钥已重置为默认', true)
    } catch (e) {
      console.error('重置网关密钥失败', e)
      toast('重置失败', false)
    }
  }

  const handleSaveSubscribe = async () => {
    if (subscribeIntervalMin < 0) {
      toast('拉取间隔不能为负数', false)
      return
    }
    try {
      await api.configSet('subscribe_url', subscribeUrl.trim())
      await api.configSet('subscribe_interval_min', String(Math.floor(subscribeIntervalMin)))
      toast('订阅配置已保存', true)
    } catch (e) {
      console.error('保存订阅配置失败', e)
      toast('保存失败', false)
    }
  }

  const handleSubscribeImport = async () => {
    if (!subscribeUrl.trim()) {
      toast('请先填写订阅 URL', false)
      return
    }
    setSubscribeBusy(true)
    try {
      const n = await api.subscribeImport(subscribeUrl.trim(), subscribeTarget === 'pool')
      toast(`订阅拉取成功：批量导入 ${n} 个实例（${subscribeTarget === 'pool' ? '已入池' : '独享'}）`, true)
    } catch (e) {
      console.error('订阅导入失败', e)
      toast(String(e), false)
    } finally {
      setSubscribeBusy(false)
    }
  }

  const handleSaveHealth = async () => {
    if (healthCheckIntervalSec < 0 || healthRestartThreshold < 1) {
      toast('巡检间隔不能为负，重启阈值至少为 1', false)
      return
    }
    try {
      await api.configSet('health_check_interval_sec', String(Math.floor(healthCheckIntervalSec)))
      await api.configSet('health_restart_threshold', String(Math.floor(healthRestartThreshold)))
      toast('健康巡检配置已保存', true)
    } catch (e) {
      console.error('保存巡检配置失败', e)
      toast('保存失败', false)
    }
  }

  const handleSaveLogFilter = async () => {
    try {
      await api.configSet('log_filter_keywords', logFilterKeywords.trim())
      toast('日志过滤已保存', true)
    } catch (e) {
      console.error('保存日志过滤失败', e)
      toast('保存失败', false)
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
    }
  }

  if (!config || !binariesInfo) {
    return <div className="p-8 text-zinc-500">加载中...</div>
  }

  return (
    <div className="p-6 space-y-6 max-w-2xl mx-auto">
      <h1 className="text-2xl font-semibold text-zinc-900">设置</h1>

      {/* Clash 外部控制 */}
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
          className="bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700"
        >
          保存
        </button>
      </div>

      {/* 网关超时切换（区间随机，防上游识别） */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">网关超时切换</h2>
        <p className="text-zinc-500 text-xs">
          每次请求在区间内随机取超时值，避免固定超时被上游识别为定时扫描；最小值防止过密重试
        </p>

        {/* 首字超时 (TTFT) */}
        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">首字超时 (TTFT)</label>
          <div className="flex items-center gap-3">
            <input
              type="number"
              min={1}
              value={timeoutForm.timeout_ttft_min_ms}
              onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_ttft_min_ms: Number(e.target.value) })}
              className="w-28 px-3 py-2 border rounded-lg"
            />
            <span className="text-zinc-400">~</span>
            <input
              type="number"
              min={1}
              value={timeoutForm.timeout_ttft_max_ms}
              onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_ttft_max_ms: Number(e.target.value) })}
              className="w-28 px-3 py-2 border rounded-lg"
            />
            <span className="text-zinc-500 text-xs">毫秒</span>
          </div>
          <p className="text-zinc-500 text-xs">建流后等待首个内容块，超时则判定异常并切换。默认 10s</p>
        </div>

        {/* 块间静默超时 */}
        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">块间静默超时</label>
          <div className="flex items-center gap-3">
            <input
              type="number"
              min={1}
              value={timeoutForm.timeout_silence_min_ms}
              onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_silence_min_ms: Number(e.target.value) })}
              className="w-28 px-3 py-2 border rounded-lg"
            />
            <span className="text-zinc-400">~</span>
            <input
              type="number"
              min={1}
              value={timeoutForm.timeout_silence_max_ms}
              onChange={(e) => setTimeoutForm({ ...timeoutForm, timeout_silence_max_ms: Number(e.target.value) })}
              className="w-28 px-3 py-2 border rounded-lg"
            />
            <span className="text-zinc-500 text-xs">毫秒</span>
          </div>
          <p className="text-zinc-500 text-xs">两个数据块之间无数据，判定卡死并切换。默认 5s</p>
        </div>

        {/* 切换前并行探测数 */}
        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">切换前并行探测数</label>
          <div className="flex items-center gap-3">
            <input
              type="number"
              min={1}
              value={timeoutForm.failover_probe_min}
              onChange={(e) => setTimeoutForm({ ...timeoutForm, failover_probe_min: Number(e.target.value) })}
              className="w-28 px-3 py-2 border rounded-lg"
            />
            <span className="text-zinc-400">~</span>
            <input
              type="number"
              min={1}
              value={timeoutForm.failover_probe_max}
              onChange={(e) => setTimeoutForm({ ...timeoutForm, failover_probe_max: Number(e.target.value) })}
              className="w-28 px-3 py-2 border rounded-lg"
            />
            <span className="text-zinc-500 text-xs">个</span>
          </div>
          <p className="text-zinc-500 text-xs">切换前并行探测候选节点数量。默认 2~3</p>
        </div>

        {/* 日志保留上限 */}
        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">调用日志保留上限</label>
          <input
            type="number"
            min={100}
            value={timeoutForm.call_log_max}
            onChange={(e) => setTimeoutForm({ ...timeoutForm, call_log_max: Number(e.target.value) })}
            className="w-28 px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">日志页最多保留的请求记录数。默认 5000</p>
        </div>

        {/* 节点前缀展示开关 */}
        <div className="flex items-center space-x-3">
          <label className="relative inline-flex items-center cursor-pointer">
            <input
              type="checkbox"
              checked={showNodePrefix}
              onChange={(e) => handleShowNodePrefixChange(e.target.checked)}
              className="sr-only peer"
            />
            <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
          </label>
          <span className="text-sm text-zinc-700">对话流首段展示「节点 · 模型」前缀</span>
        </div>
        <p className="text-zinc-500 text-xs">开启后每条回复显示由哪个实例/模型回答（切换节点时重新标注）。默认关闭</p>

        <button
          onClick={handleSaveTimeout}
          className="bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700"
        >
          保存超时配置
        </button>
      </div>

      {/* 网关与端口 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">网关与端口</h2>
        <p className="text-zinc-500 text-xs">
          修改网关端口/密钥会立即重建网关进程；HTTP 端口为 headless 模式监听端口，修改后重启管理器生效
        </p>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">统一网关端口</label>
          <input
            type="number"
            min={1}
            max={65535}
            value={gatewayPort}
            onChange={(e) => setGatewayPort(e.target.value)}
            className="w-28 px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">留空恢复默认。当前生效值：{gatewayPort || '默认'}</p>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">网关 API 密钥</label>
          <input
            type="password"
            placeholder={config.has_gateway_key ? '留空则不修改' : '至少 8 个字符'}
            value={gatewayKey}
            onChange={(e) => setGatewayKey(e.target.value)}
            className="w-full px-3 py-2 border rounded-lg"
          />
          {config.has_gateway_key && (
            <div className="flex items-center space-x-3">
              <p className="text-zinc-500 text-xs">已配置自定义密钥</p>
              <button
                onClick={handleResetGatewayKey}
                className="text-xs text-red-600 hover:underline"
              >
                重置为默认
              </button>
            </div>
          )}
          <p className="text-zinc-500 text-xs">留空则不修改；自定义后上游需带 X-API-Key 访问网关</p>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">管理器 HTTP 端口</label>
          <input
            type="number"
            min={1}
            max={65535}
            value={httpPort}
            onChange={(e) => setHttpPort(e.target.value)}
            className="w-28 px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">headless（serve）模式监听端口；留空恢复默认。桌面模式固定 127.0.0.1:19090</p>
        </div>

        <button
          onClick={handleSaveGateway}
          className="bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700"
        >
          保存网关配置
        </button>
      </div>

      {/* 订阅拉取 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">订阅拉取</h2>
        <p className="text-zinc-500 text-xs">从 Clash 订阅链接拉取节点并批量导入为实例</p>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">订阅 URL</label>
          <input
            type="text"
            placeholder="https://example.com/subscribe"
            value={subscribeUrl}
            onChange={(e) => setSubscribeUrl(e.target.value)}
            className="w-full px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">留空则不自动拉取</p>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">自动拉取间隔（分钟）</label>
          <input
            type="number"
            min={0}
            value={subscribeIntervalMin}
            onChange={(e) => setSubscribeIntervalMin(Number(e.target.value))}
            className="w-28 px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">0 = 关闭自动拉取</p>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">立即拉取批量导入为实例</label>
          <div className="flex items-center gap-3">
            <div className="flex items-center rounded-lg border border-zinc-200 bg-white p-0.5">
              <button
                onClick={() => setSubscribeTarget('solo')}
                className={clsx(
                  'px-3 py-1 rounded-md text-[13px] transition-colors',
                  subscribeTarget === 'solo' ? 'bg-zinc-900 text-white' : 'text-zinc-500 hover:bg-zinc-100',
                )}
                title="导入为独享实例（一人一实例，默认）"
              >
                独享
              </button>
              <button
                onClick={() => setSubscribeTarget('pool')}
                className={clsx(
                  'px-3 py-1 rounded-md text-[13px] transition-colors',
                  subscribeTarget === 'pool' ? 'bg-zinc-900 text-white' : 'text-zinc-500 hover:bg-zinc-100',
                )}
                title="导入并标记进实例池（聚合到统一网关）"
              >
                进池
              </button>
            </div>
            <button
              onClick={() => void handleSubscribeImport()}
              disabled={subscribeBusy}
              className="flex items-center gap-1.5 bg-green-600 text-white rounded-lg px-4 py-2 hover:bg-green-700 disabled:opacity-60 disabled:cursor-not-allowed"
            >
              {subscribeBusy ? '拉取中…' : '一键拉取并导入'}
            </button>
          </div>
          <p className="text-zinc-500 text-xs">立即从上方订阅 URL 拉取并批量导入为实例，目标可选「独享/进池」；仅拉取节点请到「节点池」页</p>
        </div>

        <button
          onClick={handleSaveSubscribe}
          className="bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700"
        >
          保存订阅配置
        </button>
      </div>

      {/* 健康巡检 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">健康巡检</h2>
        <p className="text-zinc-500 text-xs">定期探测运行中实例，连续失败达到阈值自动重启</p>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">巡检间隔（秒）</label>
          <input
            type="number"
            min={0}
            value={healthCheckIntervalSec}
            onChange={(e) => setHealthCheckIntervalSec(Number(e.target.value))}
            className="w-28 px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">0 = 关闭巡检</p>
        </div>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">连续失败重启阈值</label>
          <input
            type="number"
            min={1}
            value={healthRestartThreshold}
            onChange={(e) => setHealthRestartThreshold(Number(e.target.value))}
            className="w-28 px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">连续探测失败达到该次数则自动重启实例</p>
        </div>

        <button
          onClick={handleSaveHealth}
          className="bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700"
        >
          保存巡检配置
        </button>
      </div>

      {/* 日志过滤 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">日志过滤</h2>
        <p className="text-zinc-500 text-xs">按关键词过滤调用日志页显示内容，屏蔽干扰噪音</p>

        <div className="space-y-2">
          <label className="block text-sm font-medium text-zinc-700">过滤关键词</label>
          <input
            type="text"
            placeholder="error,timeout,429（逗号分隔）"
            value={logFilterKeywords}
            onChange={(e) => setLogFilterKeywords(e.target.value)}
            className="w-full px-3 py-2 border rounded-lg"
          />
          <p className="text-zinc-500 text-xs">命中任一关键词的记录将被隐藏；留空显示全部</p>
        </div>

        <button
          onClick={handleSaveLogFilter}
          className="bg-zinc-900 text-white rounded-lg px-4 py-2 hover:bg-zinc-700"
        >
          保存日志过滤
        </button>
      </div>

      {/* 开机自启 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">开机自启</h2>
        
        <div className="flex items-center space-x-3">
          <label className="relative inline-flex items-center cursor-pointer">
            <input
              type="checkbox"
              checked={autostart}
              onChange={(e) => handleAutostartChange(e.target.checked)}
              className="sr-only peer"
            />
            <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-zinc-900"></div>
          </label>
          <span className="text-sm text-zinc-700">开机时自动启动管理器</span>
        </div>
        <p className="text-zinc-500 text-xs">
          {binariesInfo?.platform === 'windows'
            ? 'Windows 注册表'
            : '当前平台暂不支持开机自启（可配置 systemd 服务）'}
        </p>
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
            className="px-4 py-2 rounded-lg border border-zinc-300 text-sm hover:bg-zinc-100"
          >
            清理运行数据
          </button>
          <button
            onClick={() => handleDataClean(2)}
            className="px-4 py-2 rounded-lg border border-amber-300 text-sm text-amber-700 hover:bg-amber-50"
          >
            清空实例记录
          </button>
          <button
            onClick={() => handleDataClean(3)}
            className="px-4 py-2 rounded-lg bg-red-600 text-white text-sm hover:bg-red-700"
          >
            全部重置
          </button>
        </div>
        <p className="text-zinc-500 text-xs">全部重置会删除 config.json（备份为 config.json.bak），需重新配置</p>
      </div>

      {/* 关于 */}
      <div className="bg-white rounded-2xl border p-5 space-y-4">
        <h2 className="text-lg font-medium text-zinc-900">关于</h2>
        
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
              <span>{binariesInfo?.platform === 'windows' ? 'opencode2api.exe' : 'opencode2api'}</span>
            </div>
            <div className="flex items-center space-x-2 text-sm">
              <span className={binariesInfo.sb_exists ? 'text-green-600' : 'text-red-600'}>
                {binariesInfo.sb_exists ? '✓' : '✗'}
              </span>
              <span>{binariesInfo?.platform === 'windows' ? 'sing-box.exe' : 'sing-box'}</span>
            </div>
          </div>
        </div>

        <p className="text-zinc-500 text-xs">子程序随主程序内嵌，运行时不满足时自动释放</p>
      </div>
    </div>
  )
}