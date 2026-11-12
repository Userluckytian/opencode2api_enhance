/**
 * 供应商维度（阶段 L）。
 *
 * 三类口径与后端 `core/manager/calllog_vendor.go` 的 `ClassifyVendor` **同款规则**，
 * 两处注释互相指向 —— 改动规则时必须同步改两处，否则日志页、统计页、后端
 * `/api/admin/call-log/filtered` 会出现同一条记录被归到不同类的漂移。
 */

import type { CallLogRecord } from './api'

export type VendorKind = 'kilo' | 'opencode' | 'custom' | ''

export const VENDOR_OPTIONS: { value: VendorKind; label: string }[] = [
  { value: '', label: '全部供应商' },
  { value: 'kilo', label: 'Kilo' },
  { value: 'opencode', label: 'OpenCode' },
  { value: 'custom', label: '自定义' },
]

export const VENDOR_LABEL: Record<Exclude<VendorKind, ''>, string> = {
  kilo: 'Kilo',
  opencode: 'OpenCode',
  custom: '自定义',
}

/** 把一条调用日志归到 kilo / opencode / custom（后端 ClassifyVendor 的前端镜像）。 */
export function vendorOf(r: CallLogRecord): Exclude<VendorKind, ''> {
  const v = (r.vendor ?? '').trim().toLowerCase()
  if (v === 'kilo') return 'kilo'
  if (v === 'opencode') return 'opencode'

  const src = r.source ?? ''
  if (src.includes('引擎') || src.toLowerCase().includes('engine')) return 'opencode'
  if (src.includes('Kilo')) return 'kilo'

  const modelHasSlash = (r.model ?? '').includes('/')
  switch (r.route_verdict) {
    case 'proxied':
    case 'no_healthy_node':
      return 'opencode'
    case 'direct_by_design':
    case 'direct_config_missing':
    case 'direct_unexpected':
      return modelHasSlash ? 'custom' : 'opencode'
  }
  if (v !== '') return 'custom'
  if (modelHasSlash) return 'custom'
  return 'opencode'
}

/** 筛选：空串 = 不过滤。 */
export function vendorMatches(r: CallLogRecord, filter: VendorKind): boolean {
  return filter === '' || vendorOf(r) === filter
}
