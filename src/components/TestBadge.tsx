import clsx from 'clsx'
import type { TestResult } from '../lib/api'

/**
 * 实例池 / 独享节点的测试结果徽章。
 * 分类与文案由后端 classifyProbeOutcome 统一生成，前端只做枚举 → 颜色映射。
 */
export function testBadge(r?: TestResult) {
  if (!r) return null
  const category = r.category || (r.ok ? 'ok' : 'other')
  const map: Record<string, [string, string]> = {
    ok: ['bg-green-50 text-green-700', '✓'],
    rate_limit: ['bg-amber-50 text-amber-700', '限流'],
    upstream: ['bg-amber-50 text-amber-700', '上游'],
    timeout: ['bg-red-50 text-red-600', '超时'],
    config: ['bg-red-50 text-red-600', '配置'],
    socks: ['bg-red-50 text-red-600', '链路'],
    tls: ['bg-red-50 text-red-600', 'TLS'],
    other: ['bg-red-50 text-red-600', '失败'],
  }
  const [cl, label] = map[category] ?? ['bg-red-50 text-red-600', '失败']
  return (
    <span
      className={clsx('inline-block max-w-[240px] px-2 py-0.5 rounded-full text-[11px] font-medium truncate', cl)}
      title={r.message || (r.ok ? '测试通过' : '测试失败')}
    >
      {r.ok ? `${label} ${r.latency_ms}ms${r.message ? ` · ${r.message}` : ''}` : `${label} · ${r.message || '失败'}`}
    </span>
  )
}
