// 更新检查：直接读 GitHub Releases（无需鉴权），按语义化版本比较，
// 在所有 release 中取「大于当前版本的最大版本」，而不是只看 GitHub 的 latest
//（latest 在预发布/补发场景下可能不是最高版本）。
import { useCallback, useEffect, useRef, useState } from 'react'

export const UPDATE_REPO = 'Userluckytian/opencode2api_enhance'
const RELEASES_URL = `https://api.github.com/repos/${UPDATE_REPO}/releases?per_page=100`
const CACHE_KEY = 'oc2api.updateCheck.v1'
const DISMISS_KEY = 'oc2api.updateDismissedVersion'
const CACHE_TTL_MS = 60 * 60 * 1000
const CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000
const FETCH_TIMEOUT_MS = 8000

export type UpdateInfo = {
  current: string
  latest: string
  tag: string
  name?: string
  url: string
  publishedAt?: string
  prerelease: boolean
  body?: string
  checkedAt: number
  fromCache?: boolean
}

type GitHubRelease = {
  tag_name?: string
  name?: string
  html_url?: string
  published_at?: string
  prerelease?: boolean
  draft?: boolean
  body?: string
}

type Cache = { checkedAt: number; current: string; latest: UpdateInfo | null }

/** 去 v 前缀与 build metadata（1.2.3+build → 1.2.3）。 */
export function normalizeVersion(v: string): string {
  return v.trim().replace(/^v/i, '').split('+')[0] || ''
}

type ParsedVersion = { core: [number, number, number]; pre: string[] }

export function parseVersion(v: string): ParsedVersion | null {
  const s = normalizeVersion(v)
  const m = /^(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?$/.exec(s)
  if (!m) return null
  const core: [number, number, number] = [Number(m[1]), Number(m[2] || 0), Number(m[3] || 0)]
  const pre = m[4] ? m[4].split('.').filter(Boolean) : []
  return { core, pre }
}

/** 语义化版本比较：a>b 返回正数，a<b 返回负数，相等返回 0。 */
export function compareSemver(a: string, b: string): number {
  const pa = parseVersion(a)
  const pb = parseVersion(b)
  if (!pa && !pb) return 0
  if (!pa) return -1
  if (!pb) return 1
  for (let i = 0; i < 3; i++) {
    const d = (pa.core[i] as number) - (pb.core[i] as number)
    if (d !== 0) return d
  }
  // 正式版 > 预发布版
  if (pa.pre.length === 0 && pb.pre.length === 0) return 0
  if (pa.pre.length === 0) return 1
  if (pb.pre.length === 0) return -1
  const n = Math.min(pa.pre.length, pb.pre.length)
  for (let i = 0; i < n; i++) {
    const x = pa.pre[i] as string
    const y = pb.pre[i] as string
    const xn = /^\d+$/.test(x)
    const yn = /^\d+$/.test(y)
    if (xn && yn) {
      const d = Number(x) - Number(y)
      if (d !== 0) return d
    } else if (xn !== yn) {
      // 数字标识符优先级低于字母标识符
      return xn ? -1 : 1
    } else {
      const d = x.localeCompare(y)
      if (d !== 0) return d
    }
  }
  // 前缀相同：字段更多者优先级更高
  return pa.pre.length - pb.pre.length
}

/** 在所有非 draft release 中，选出大于 current 的最高版本。 */
export function pickLatestRelease(releases: GitHubRelease[], current: string): GitHubRelease | null {
  let best: { rel: GitHubRelease; ver: string } | null = null
  for (const rel of releases) {
    if (rel.draft) continue
    const tag = (rel.tag_name || '').trim()
    if (!tag) continue
    const ver = normalizeVersion(tag)
    if (!parseVersion(ver)) continue
    if (compareSemver(ver, current) <= 0) continue
    if (!best || compareSemver(ver, best.ver) > 0) best = { rel, ver }
  }
  return best?.rel ?? null
}

function readCache(): Cache | null {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    if (!raw) return null
    const c = JSON.parse(raw) as Cache
    if (!c || typeof c.checkedAt !== 'number' || typeof c.current !== 'string') return null
    return c
  } catch {
    return null
  }
}

function writeCache(c: Cache) {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify(c))
  } catch {
    /* 隐私模式/配额不足：忽略缓存 */
  }
}

function readDismissed(): string | null {
  try {
    return localStorage.getItem(DISMISS_KEY)
  } catch {
    return null
  }
}

function writeDismissed(version: string) {
  try {
    localStorage.setItem(DISMISS_KEY, version)
  } catch {
    /* ignore */
  }
}

type CheckResult = { update: UpdateInfo | null; checkedAt: number; fromCache: boolean }

async function fetchReleases(current: string, force: boolean): Promise<CheckResult> {
  const cached = readCache()
  const now = Date.now()
  if (!force && cached && cached.current === current && now - cached.checkedAt < CACHE_TTL_MS) {
    return { update: cached.latest, checkedAt: cached.checkedAt, fromCache: true }
  }

  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), FETCH_TIMEOUT_MS)
  try {
    const res = await fetch(RELEASES_URL, {
      headers: { Accept: 'application/vnd.github+json' },
      signal: ctrl.signal,
      cache: 'no-store',
    })
    if (!res.ok) throw new Error(`GitHub API ${res.status}`)
    const releases = (await res.json()) as GitHubRelease[]
    if (!Array.isArray(releases)) throw new Error('GitHub Releases 返回格式异常')
    const rel = pickLatestRelease(releases, current)
    const info: UpdateInfo | null = rel
      ? {
          current,
          latest: normalizeVersion(rel.tag_name || ''),
          tag: (rel.tag_name || '').trim(),
          name: rel.name,
          url: rel.html_url || `https://github.com/${UPDATE_REPO}/releases`,
          publishedAt: rel.published_at,
          prerelease: !!rel.prerelease,
          body: rel.body,
          checkedAt: now,
        }
      : null
    writeCache({ checkedAt: now, current, latest: info })
    return { update: info, checkedAt: now, fromCache: false }
  } catch (e) {
    // 启动检查失败时静默回退到旧缓存；手动检查把错误抛给 UI。
    if (!force && cached && cached.current === current) {
      return { update: cached.latest, checkedAt: cached.checkedAt, fromCache: true }
    }
    throw e
  } finally {
    clearTimeout(timer)
  }
}

/** App 级更新检查：启动检查一次 + 每 6 小时自动检查；返回当前可见的新版本。 */
export function useUpdateCheck(current: string) {
  const [update, setUpdate] = useState<UpdateInfo | null>(null)
  const [checking, setChecking] = useState(false)
  const [checkedAt, setCheckedAt] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [dismissedVersion, setDismissedVersion] = useState<string | null>(readDismissed())
  const inFlightRef = useRef<Promise<CheckResult | null> | null>(null)

  const apply = useCallback((info: UpdateInfo | null) => {
    setUpdate(info)
  }, [])

  const check = useCallback(
    async (force = false): Promise<CheckResult | null> => {
      if (!force && inFlightRef.current) return inFlightRef.current
      const promise = (async () => {
        setChecking(true)
        setError(null)
        try {
          const r = await fetchReleases(current, force)
          apply(r.update)
          setCheckedAt(r.checkedAt)
          return r
        } catch (e) {
          setError(e instanceof Error ? e.message : '检查更新失败')
          return null
        } finally {
          setChecking(false)
        }
      })()
      inFlightRef.current = promise
      try {
        return await promise
      } finally {
        if (inFlightRef.current === promise) inFlightRef.current = null
      }
    },
    [apply, current],
  )

  useEffect(() => {
    void check(false)
    const timer = window.setInterval(() => void check(false), CHECK_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [check])

  const dismiss = useCallback((version: string) => {
    writeDismissed(version)
    setDismissedVersion(version)
  }, [])

  // 横幅只展示「未忽略且比忽略版本更新」的提示；设置页始终能看到原始 update。
  const bannerUpdate =
    update && (!dismissedVersion || compareSemver(update.latest, dismissedVersion) > 0) ? update : null

  return { update, bannerUpdate, checking, checkedAt, error, check, dismiss }
}
