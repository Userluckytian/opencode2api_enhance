// Tauri 官方 updater 的薄封装：检查 → 下载（带进度）→ 安装 → 重启。
//
// 设计要点：
// - 只在桌面壳（Tauri WebView）里可用；headless Web 版 / 普通浏览器直接降级到「打开发布页」。
// - 下载与安装拆开：先 download() 拿到安装包，再由用户点「立即安装」，期间可关闭弹窗。
//   已下载的数据由 Rust 侧资源持有，仅本次进程有效——退出应用后需重新下载。
// - Windows 的 NSIS 安装是静默的（不弹安装向导），安装前会自动退出应用、装完自动重启；
//   安装包由系统写到 %TEMP%，没有常驻缓存。
import { isDesktop } from './env'
import type { Update } from '@tauri-apps/plugin-updater'

export type UpdatePhase = 'idle' | 'checking' | 'downloading' | 'ready' | 'installing' | 'error'

export type UpdateProgress = {
  phase: UpdatePhase
  downloaded: number
  total: number | null
  percent: number | null
  message?: string
}

export type NativeUpdateInfo = {
  version: string
  currentVersion: string
  date?: string
  body?: string
}

type Pending = { version: string; update: Update }

let pending: Pending | null = null
let downloadedVersion: string | null = null
let downloadPromise: Promise<void> | null = null
let checkPromise: Promise<NativeUpdateInfo | null> | null = null

export function nativeUpdateSupported(): boolean {
  return isDesktop
}

function isWindows(): boolean {
  return typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent)
}

/** 已下载完成、可直接安装的版本（未下载返回 null）。 */
export function downloadedNativeVersion(): string | null {
  return downloadedVersion
}

/** 当前 pending 的版本（可能尚未下载）。 */
export function pendingNativeVersion(): string | null {
  return pending?.version ?? null
}

export function clearPendingNativeUpdate(): void {
  if (pending) {
    const p = pending
    pending = null
    downloadedVersion = null
    void p.update.close().catch(() => {})
  }
}

async function loadPlugin() {
  return import('@tauri-apps/plugin-updater')
}

/**
 * 查询 updater 清单（latest.json）。返回 null 表示当前已是最新，或清单里没有更高版本。
 * 复用已存在的 Update 资源，避免同版本反复 check 造成资源泄漏。
 */
export function checkNativeUpdate(): Promise<NativeUpdateInfo | null> {
  if (!checkPromise) {
    checkPromise = doCheckNativeUpdate().finally(() => {
      checkPromise = null
    })
  }
  return checkPromise
}

async function doCheckNativeUpdate(): Promise<NativeUpdateInfo | null> {
  if (!isDesktop) return null
  const { check } = await loadPlugin()
  const found = await check({ timeout: 30_000 })
  if (!found) return null

  // 同版本：保留已有资源（可能已下载），关掉新句柄。
  if (pending && pending.version === found.version) {
    await found.close().catch(() => {})
    return {
      version: pending.version,
      currentVersion: found.currentVersion,
      date: found.date,
      body: found.body,
    }
  }

  // 版本变化：释放旧版本及其已下载数据。
  if (pending) {
    const old = pending
    pending = null
    downloadedVersion = null
    await old.update.close().catch(() => {})
  }
  pending = { version: found.version, update: found }
  return {
    version: found.version,
    currentVersion: found.currentVersion,
    date: found.date,
    body: found.body,
  }
}

/** 下载当前 pending 的安装包；进度通过回调上报。重复调用共用同一个下载。 */
export function downloadNativeUpdate(onProgress: (p: UpdateProgress) => void): Promise<void> {
  if (downloadPromise) return downloadPromise
  const entry = pending
  if (!entry) return Promise.reject(new Error('尚未获取到可安装的版本，请重新检查更新'))

  let total: number | null = null
  let downloaded = 0
  onProgress({ phase: 'downloading', downloaded: 0, total: null, percent: null })

  downloadPromise = entry.update
    .download((event) => {
      if (event.event === 'Started') {
        total = event.data.contentLength ?? null
        onProgress({ phase: 'downloading', downloaded, total, percent: total ? 0 : null })
      } else if (event.event === 'Progress') {
        downloaded += event.data.chunkLength
        onProgress({
          phase: 'downloading',
          downloaded,
          total,
          percent: total && total > 0 ? Math.min(99, (downloaded / total) * 100) : null,
        })
      }
    })
    .then(() => {
      downloadedVersion = entry.version
      onProgress({ phase: 'ready', downloaded, total, percent: 100 })
    })
    .finally(() => {
      downloadPromise = null
    })

  return downloadPromise
}

/**
 * 安装已下载的更新。
 * - Windows：调用后应用会被安装器结束，install() 的 Promise 通常不会返回。
 * - macOS / Linux：安装后需要主动 relaunch 才会运行新版本。
 */
export async function installNativeUpdate(): Promise<void> {
  const entry = pending
  if (!entry) throw new Error('没有待安装的更新，请重新检查')
  if (downloadedVersion !== entry.version) throw new Error('安装包尚未下载完成')

  await entry.update.install()
  if (!isWindows()) {
    const { relaunch } = await import('@tauri-apps/plugin-process')
    await relaunch()
  }
}

/** 手动重启（仅桌面壳可用）。 */
export async function relaunchApp(): Promise<void> {
  const { relaunch } = await import('@tauri-apps/plugin-process')
  await relaunch()
}
