// 应用内更新弹窗：检查清单 → 下载（进度条）→ 立即安装 / 稍后。
//
// 安装前先通过 core 的 HTTP 接口停止运行中的实例与统一网关，避免安装器替换文件时
// 被 Go 子进程占用；Windows 安装器随后结束应用并在安装完成后自动重启。
import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { AlertTriangle, CheckCircle2, Download, Loader2, RefreshCw, Sparkles, X } from 'lucide-react'
import { api } from '../lib/api'
import { UPDATE_REPO, type UpdateInfo } from '../lib/updates'
import MarkdownLite from './MarkdownLite'
import {
  checkNativeUpdate,
  downloadNativeUpdate,
  downloadedNativeVersion,
  pendingNativeVersion,
  installNativeUpdate,
  type NativeUpdateInfo,
  type UpdateProgress,
} from '../lib/nativeUpdate'

type Step = 'checking' | 'confirm' | 'downloading' | 'ready' | 'installing' | 'error'

const EMPTY_PROGRESS: UpdateProgress = { phase: 'idle', downloaded: 0, total: null, percent: null }

function fmtSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 MB'
  const mb = bytes / 1024 / 1024
  return mb >= 100 ? `${mb.toFixed(0)} MB` : `${mb.toFixed(1)} MB`
}

export default function UpdateModal({
  open,
  update,
  onClose,
  onOpenRelease,
  toast,
}: {
  open: boolean
  update: UpdateInfo | null
  onClose: () => void
  onOpenRelease: (url: string) => void
  toast: (msg: string, ok?: boolean) => void
}) {
  const [step, setStep] = useState<Step>('checking')
  const [info, setInfo] = useState<NativeUpdateInfo | null>(null)
  const [progress, setProgress] = useState<UpdateProgress>(EMPTY_PROGRESS)
  const [error, setError] = useState<string | null>(null)
  const [prep, setPrep] = useState<string | null>(null)

  const locked = step === 'downloading' || step === 'installing'

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setStep('checking')
    setInfo(null)
    setProgress(EMPTY_PROGRESS)
    setError(null)
    setPrep(null)
    ;(async () => {
      try {
        const r = await checkNativeUpdate()
        if (cancelled) return
        if (!r) {
          const dv = downloadedNativeVersion()
          if (dv && pendingNativeVersion() === dv) {
            setInfo({ version: dv, currentVersion: update?.current || '' })
            setStep('ready')
            setProgress({ phase: 'ready', downloaded: 0, total: null, percent: 100 })
            return
          }
          setStep('error')
          setError('更新清单中暂时没有比当前更高的版本（可能刚发布、清单还在同步），请稍后重试。')
          return
        }
        setInfo(r)
        if (downloadedNativeVersion() === r.version) {
          setStep('ready')
          setProgress({ phase: 'ready', downloaded: 0, total: null, percent: 100 })
        } else {
          setStep('confirm')
        }
      } catch (e) {
        if (cancelled) return
        // 已下载的安装包不依赖网络：清单拉取失败时仍允许直接安装。
        const dv = downloadedNativeVersion()
        if (dv && pendingNativeVersion() === dv) {
          setInfo({ version: dv, currentVersion: update?.current || '' })
          setStep('ready')
          setProgress({ phase: 'ready', downloaded: 0, total: null, percent: 100 })
          return
        }
        setStep('error')
        setError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [open])

  const handleDownload = async () => {
    setStep('downloading')
    setError(null)
    try {
      await downloadNativeUpdate((p) => setProgress(p))
      setStep('ready')
    } catch (e) {
      setStep('error')
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleInstall = async () => {
    setStep('installing')
    setError(null)
    try {
      // 停止实例/网关是「尽力而为」：core 不可用时不应阻塞安装。
      try {
        setPrep('正在停止运行中的实例…')
        const list = await api.listInstances()
        const names = list
          .filter((i) => i.status === 'Running' || i.status === 'Starting')
          .map((i) => i.name)
        if (names.length > 0) await api.batchStop(names)

        setPrep('正在停止统一网关…')
        await api.gatewayStop().catch(() => {})
      } catch {
        /* 管理接口不可用：直接进入安装 */
      }

      setPrep('正在退出并启动安装程序…')
      // 给 core/实例一点退出时间，避免安装器替换被占用的二进制
      await new Promise((r) => setTimeout(r, 1500))
      await installNativeUpdate()
    } catch (e) {
      setStep('error')
      setPrep(null)
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleClose = () => {
    if (locked) return
    if (step === 'ready') toast('安装包已下载，稍后可在「设置 → 版本与更新」中安装', true)
    onClose()
  }

  if (!open) return null

  const percent = progress.percent
  const version = info?.version || update?.latest || ''
  // native updater 只给版本号，不给 release URL；按版本号拼出发布页，
  // 避免 GitHub 检查失败（update=null）时「打开发布页」变成死按钮。
  const releaseUrl =
    update?.url ||
    (version
      ? `https://github.com/${UPDATE_REPO}/releases/tag/v${version.replace(/^v/i, '')}`
      : '')

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-zinc-900/40 backdrop-blur-sm p-4">
      <div className="w-full max-w-md rounded-2xl bg-white shadow-2xl border border-zinc-200 overflow-hidden">
        <div className="flex items-center gap-2 px-5 py-4 border-b border-zinc-100">
          <Sparkles size={16} className="text-amber-600" />
          <h3 className="text-[15px] font-semibold text-zinc-900">应用更新</h3>
          <span className="flex-1" />
          <button
            type="button"
            onClick={handleClose}
            disabled={locked}
            title={locked ? '下载/安装中不可关闭' : '关闭'}
            className="p-1 rounded-md text-zinc-400 hover:text-zinc-700 hover:bg-zinc-100 disabled:opacity-30 disabled:cursor-not-allowed"
          >
            <X size={16} />
          </button>
        </div>

        <div className="px-5 py-4 space-y-4">
          <div className="text-[13px] text-zinc-600">
            {version ? (
              <>
                新版本 <b className="text-zinc-900">v{version}</b>
                {info?.currentVersion ? <span className="text-zinc-400">（当前 v{info.currentVersion}）</span> : null}
              </>
            ) : (
              '正在检查更新…'
            )}
            {update?.prerelease ? (
              <span className="ml-1.5 px-1.5 py-0.5 rounded bg-amber-100 text-amber-800 text-[10px] align-middle">预发布</span>
            ) : null}
          </div>

          {step === 'checking' && (
            <div className="flex items-center gap-2 text-[13px] text-zinc-500 py-2">
              <Loader2 size={15} className="animate-spin" />
              正在读取更新清单…
            </div>
          )}

          {step === 'confirm' && (
            <div className="space-y-3">
              <p className="text-[12px] leading-relaxed text-zinc-500">
                下载完成后会提示「立即安装」。安装时会先停止运行中的实例与统一网关，然后关闭应用并启动安装程序；
                Windows 会显示安装进度窗口（管理员安装需要 UAC 授权），安装完成后自动重新打开。
              </p>
              {info?.body ? (
                <div className="max-h-48 overflow-y-auto rounded-lg bg-zinc-50 border border-zinc-100 p-3 text-[12px] leading-relaxed text-zinc-600">
                  <MarkdownLite text={info.body} onOpenUrl={onOpenRelease} />
                </div>
              ) : null}
            </div>
          )}

          {step === 'downloading' && (
            <div className="space-y-2">
              <div className="flex items-center justify-between text-[12px] text-zinc-600">
                <span>正在下载安装包…</span>
                <span className="font-mono">
                  {fmtSize(progress.downloaded)}
                  {progress.total ? ` / ${fmtSize(progress.total)}` : ''}
                  {percent != null ? ` · ${percent.toFixed(0)}%` : ''}
                </span>
              </div>
              <div className="h-2 rounded-full bg-zinc-100 overflow-hidden">
                <div
                  className={clsx('h-full bg-amber-500 transition-all duration-200', percent == null && 'animate-pulse')}
                  style={{ width: percent != null ? `${Math.max(2, percent)}%` : '30%' }}
                />
              </div>
              <p className="text-[11px] text-zinc-400">请保持应用开启；下载期间窗口不可关闭。</p>
            </div>
          )}

          {step === 'ready' && (
            <div className="space-y-2">
              <div className="flex items-center gap-2 text-[13px] text-green-700">
                <CheckCircle2 size={15} />
                安装包已下载完成
                {progress.total ? <span className="text-zinc-400 font-mono text-[12px]">· {fmtSize(progress.total)}</span> : null}
              </div>
              <p className="text-[12px] leading-relaxed text-zinc-500">
                安装包保存在本次运行内（退出应用后需重新下载）。点击「立即安装」后应用会退出并运行安装程序；
                安装完成后自动重启，实例与网关不会自动恢复运行，请在安装后手动启动。
              </p>
            </div>
          )}

          {step === 'installing' && (
            <div className="space-y-2">
              <div className="flex items-center gap-2 text-[13px] text-zinc-700">
                <Loader2 size={15} className="animate-spin" />
                {prep || '正在准备安装…'}
              </div>
              <p className="text-[11px] text-zinc-400">应用即将关闭，请勿手动结束安装进程。</p>
            </div>
          )}

          {step === 'error' && (
            <div className="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2.5 text-[12px] text-red-700">
              <AlertTriangle size={15} className="shrink-0 mt-0.5" />
              <span className="break-all">{error || '更新失败'}</span>
            </div>
          )}

          {step === 'installing' && prep && (
            <p className="text-[11px] text-zinc-400">
              统一网关停止后，正在使用网关的客户端会短暂不可用。
            </p>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 px-5 py-3.5 bg-zinc-50/70 border-t border-zinc-100">
          <button
            type="button"
            onClick={() => releaseUrl && onOpenRelease(releaseUrl)}
            disabled={!releaseUrl}
            title={releaseUrl || '暂无发布页'}
            className="mr-auto text-[12px] text-zinc-500 hover:text-zinc-800 hover:underline disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:no-underline"
          >
            打开发布页
          </button>

          {step === 'confirm' && (
            <>
              <button
                type="button"
                onClick={handleClose}
                className="px-3.5 py-1.5 rounded-lg border border-zinc-300 text-[13px] text-zinc-700 hover:bg-zinc-100"
              >
                稍后
              </button>
              <button
                type="button"
                onClick={() => void handleDownload()}
                className="flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-amber-600 text-white text-[13px] hover:bg-amber-700"
              >
                <Download size={14} />
                下载更新
              </button>
            </>
          )}

          {step === 'downloading' && (
            <span className="flex items-center gap-1.5 px-3.5 py-1.5 text-[12px] text-zinc-500">
              <Loader2 size={13} className="animate-spin" />
              下载中…
            </span>
          )}

          {step === 'ready' && (
            <>
              <button
                type="button"
                onClick={handleClose}
                className="px-3.5 py-1.5 rounded-lg border border-zinc-300 text-[13px] text-zinc-700 hover:bg-zinc-100"
              >
                稍后
              </button>
              <button
                type="button"
                onClick={() => void handleInstall()}
                className="px-3.5 py-1.5 rounded-lg bg-amber-600 text-white text-[13px] hover:bg-amber-700"
              >
                立即安装
              </button>
            </>
          )}

          {step === 'installing' && (
            <span className="flex items-center gap-1.5 px-3.5 py-1.5 text-[12px] text-zinc-500">
              <Loader2 size={13} className="animate-spin" />
              安装中…
            </span>
          )}

          {step === 'error' && (
            <>
              <button
                type="button"
                onClick={handleClose}
                className="px-3.5 py-1.5 rounded-lg border border-zinc-300 text-[13px] text-zinc-700 hover:bg-zinc-100"
              >
                关闭
              </button>
              <button
                type="button"
                onClick={() => {
                  setStep('checking')
                  setError(null)
                  void (async () => {
                    try {
                      const r = await checkNativeUpdate()
                      if (!r) {
                        setStep('error')
                        setError('更新清单中暂时没有可安装的版本，请稍后再试。')
                        return
                      }
                      setInfo(r)
                      setStep(downloadedNativeVersion() === r.version ? 'ready' : 'confirm')
                    } catch (e) {
                      setStep('error')
                      setError(e instanceof Error ? e.message : String(e))
                    }
                  })()
                }}
                className="flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-zinc-900 text-white text-[13px] hover:bg-zinc-700"
              >
                <RefreshCw size={13} />
                重试
              </button>
            </>
          )}

          {step === 'checking' && (
            <button
              type="button"
              onClick={handleClose}
              className="px-3.5 py-1.5 rounded-lg border border-zinc-300 text-[13px] text-zinc-700 hover:bg-zinc-100"
            >
              取消
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
