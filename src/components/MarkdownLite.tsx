// 轻量 Markdown 渲染：只覆盖 Release Notes 实际用到的语法
// （标题 / 引用 / 列表 / 粗体 / 行内代码 / 链接 / 分隔线），避免引入额外依赖。
import type { ReactNode } from 'react'

type Props = {
  text: string
  onOpenUrl?: (url: string) => void
}

/** 行内语法：**bold** / `code` / [text](url)。 */
function renderInline(text: string, onOpenUrl?: (url: string) => void): ReactNode[] {
  const nodes: ReactNode[] = []
  const re = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)]+\))/g
  let last = 0
  let key = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text))) {
    if (m.index > last) nodes.push(text.slice(last, m.index))
    const tok = m[0]
    if (tok.startsWith('**')) {
      nodes.push(
        <strong key={key++} className="font-semibold text-zinc-800">
          {tok.slice(2, -2)}
        </strong>,
      )
    } else if (tok.startsWith('`')) {
      nodes.push(
        <code key={key++} className="rounded bg-zinc-200/70 px-1 py-0.5 font-mono text-[11px] text-zinc-700">
          {tok.slice(1, -1)}
        </code>,
      )
    } else {
      const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(tok)
      if (link) {
        const label = link[1] ?? ''
        const url = link[2] ?? ''
        nodes.push(
          onOpenUrl ? (
            <button
              key={key++}
              type="button"
              onClick={() => onOpenUrl(url)}
              className="text-amber-700 underline-offset-2 hover:underline"
            >
              {label}
            </button>
          ) : (
            <span key={key++} className="text-amber-700">
              {label}
            </span>
          ),
        )
      } else {
        nodes.push(tok)
      }
    }
    last = m.index + tok.length
  }
  if (last < text.length) nodes.push(text.slice(last))
  return nodes
}

export default function MarkdownLite({ text, onOpenUrl }: Props) {
  const lines = text.replace(/\r\n/g, '\n').split('\n')
  const out: ReactNode[] = []
  let i = 0
  let key = 0

  while (i < lines.length) {
    const line = lines[i] ?? ''
    const trimmed = line.trim()
    if (!trimmed) {
      i++
      continue
    }

    if (/^---+$/.test(trimmed)) {
      out.push(<hr key={key++} className="my-2 border-zinc-200" />)
      i++
      continue
    }

    if (line.startsWith('### ')) {
      out.push(
        <div key={key++} className="mt-2 mb-1 text-[12px] font-semibold text-zinc-800">
          {renderInline(line.slice(4), onOpenUrl)}
        </div>,
      )
      i++
      continue
    }
    if (line.startsWith('## ')) {
      out.push(
        <div key={key++} className="mt-2 mb-1 text-[13px] font-semibold text-zinc-900">
          {renderInline(line.slice(3), onOpenUrl)}
        </div>,
      )
      i++
      continue
    }
    if (line.startsWith('# ')) {
      out.push(
        <div key={key++} className="mt-2 mb-1 text-[13px] font-semibold text-zinc-900">
          {renderInline(line.slice(2), onOpenUrl)}
        </div>,
      )
      i++
      continue
    }

    if (line.startsWith('> ')) {
      const buf: string[] = []
      while (i < lines.length) {
        const cur = lines[i] ?? ''
        if (!cur.startsWith('> ')) break
        buf.push(cur.slice(2))
        i++
      }
      out.push(
        <blockquote key={key++} className="my-1 border-l-2 border-zinc-300 pl-2 text-zinc-500">
          {buf.map((item, idx) => (
            <div key={idx} className="my-0.5">
              {renderInline(item, onOpenUrl)}
            </div>
          ))}
        </blockquote>,
      )
      continue
    }

    if (/^[-*] /.test(line)) {
      const items: string[] = []
      while (i < lines.length) {
        const cur = lines[i] ?? ''
        if (!/^[-*] /.test(cur)) break
        items.push(cur.slice(2))
        i++
      }
      out.push(
        <ul key={key++} className="my-1 list-disc space-y-0.5 pl-4">
          {items.map((item, idx) => (
            <li key={idx}>{renderInline(item, onOpenUrl)}</li>
          ))}
        </ul>,
      )
      continue
    }

    const para: string[] = [line]
    i++
    while (i < lines.length) {
      const cur = lines[i] ?? ''
      if (!cur.trim() || /^(#{1,3} |> |[-*] |---+$)/.test(cur.trim())) break
      para.push(cur)
      i++
    }
    out.push(
      <p key={key++} className="my-1">
        {para.map((item, idx) => (
          <span key={idx}>
            {idx > 0 ? ' ' : ''}
            {renderInline(item, onOpenUrl)}
          </span>
        ))}
      </p>,
    )
  }

  return <div className="space-y-0.5">{out}</div>
}
