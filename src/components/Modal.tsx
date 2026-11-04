import type { ReactNode } from 'react'
import clsx from 'clsx'
import { X } from 'lucide-react'

/**
 * 统一弹窗外壳（问题 4）：
 *   - 标题栏（含右上角 ✕）与可选 footer 是固定区，只有正文滚动；
 *   - 弹窗整体高度上限 86vh，长表单不会把关闭按钮顶出视口。
 *
 * 交互约定：默认只有 ✕ / 显式按钮关闭（防止误点遮罩丢掉未保存表单）；
 * 纯确认类弹窗可传 closeOnBackdrop 打开遮罩关闭。
 */
export default function Modal({
  open,
  title,
  subtitle,
  onClose,
  children,
  footer,
  headerExtra,
  width = 'max-w-[722px]',
  z = 'z-50',
  closeOnBackdrop = false,
  bodyClassName,
}: {
  open: boolean
  title: ReactNode
  subtitle?: ReactNode
  onClose: () => void
  children: ReactNode
  /** 常驻底部动作区（保存 / 取消等） */
  footer?: ReactNode
  /** 标题行右侧、✕ 左侧的附加操作（如「刷新模型」） */
  headerExtra?: ReactNode
  /** 最大宽度 class，默认 max-w-[722px] */
  width?: string
  /** 层级 class，默认 z-50；嵌套弹窗用 z-[60] */
  z?: string
  closeOnBackdrop?: boolean
  bodyClassName?: string
}) {
  if (!open) return null
  return (
    <div
      className={clsx('fixed inset-0 flex items-center justify-center bg-black/40 p-4', z)}
      onClick={closeOnBackdrop ? onClose : undefined}
    >
      <div
        role="dialog"
        aria-modal="true"
        className={clsx(
          'bg-white rounded-2xl shadow-xl w-full max-h-[86vh] flex flex-col overflow-hidden',
          width,
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="shrink-0 flex items-center justify-between gap-3 px-6 py-4 border-b border-zinc-100">
          <div className="min-w-0">
            <h2 className="text-lg font-semibold text-zinc-900 truncate">{title}</h2>
            {subtitle ? <p className="text-[12px] text-zinc-400 mt-0.5">{subtitle}</p> : null}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            {headerExtra}
            <button
              type="button"
              onClick={onClose}
              className="p-1.5 rounded-lg text-zinc-400 hover:bg-zinc-100 hover:text-zinc-600"
              aria-label="关闭"
              title="关闭"
            >
              <X size={18} />
            </button>
          </div>
        </div>

        <div className={clsx('flex-1 min-h-0 overflow-y-auto px-6 py-4 space-y-4', bodyClassName)}>
          {children}
        </div>

        {footer ? (
          <div className="shrink-0 flex items-center gap-2 px-6 py-3 border-t border-zinc-100 bg-white">
            {footer}
          </div>
        ) : null}
      </div>
    </div>
  )
}
