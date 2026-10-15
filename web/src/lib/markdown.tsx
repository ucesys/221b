// A small Markdown reader for Sherlock's answers: paragraphs, lists,
// fenced code, tables, `code`, **bold** and links. It builds React
// elements, never HTML, so nothing in an answer can run in the page.

import { Fragment, type ReactNode } from 'react'

export type Block =
  | { t: 'p'; text: string }
  | { t: 'h'; text: string }
  | { t: 'ul' | 'ol'; items: string[] }
  | { t: 'code'; lang: string; text: string; open: boolean }
  | { t: 'table'; head: string[]; rows: string[][] }

const fence = /^\s*(```|~~~)\s*([\w+-]*)\s*$/
const bullet = /^\s*[-*+]\s+(.*)$/
const numbered = /^\s*\d{1,3}[.)]\s+(.*)$/
const heading = /^\s*#{1,6}\s+(.*)$/
const tableRow = /^\s*\|(.*)\|\s*$/
const tableRule = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/

function cells(line: string): string[] {
  const m = tableRow.exec(line)
  return (m ? m[1]! : line).split('|').map((c) => c.trim())
}

/** Reads Markdown into blocks. An unclosed code fence (an answer still arriving) runs to the end. */
export function parseBlocks(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, '\n').split('\n')
  const out: Block[] = []
  let para: string[] = []
  const flush = () => {
    if (para.length) out.push({ t: 'p', text: para.join(' ').trim() })
    para = []
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!
    const f = fence.exec(line)
    if (f) {
      flush()
      const body: string[] = []
      let j = i + 1
      while (j < lines.length && !fence.test(lines[j]!)) body.push(lines[j++]!)
      out.push({ t: 'code', lang: f[2] ?? '', text: body.join('\n'), open: j >= lines.length })
      i = j
      continue
    }
    if (!line.trim()) {
      flush()
      continue
    }
    const h = heading.exec(line)
    if (h) {
      flush()
      out.push({ t: 'h', text: h[1]! })
      continue
    }
    if (tableRow.test(line) && i + 1 < lines.length && tableRule.test(lines[i + 1]!)) {
      flush()
      const head = cells(line)
      const rows: string[][] = []
      let j = i + 2
      while (j < lines.length && tableRow.test(lines[j]!)) rows.push(cells(lines[j++]!))
      out.push({ t: 'table', head, rows })
      i = j - 1
      continue
    }
    const b = bullet.exec(line)
    const n = b ? null : numbered.exec(line)
    if (b || n) {
      flush()
      const kind = b ? 'ul' : 'ol'
      const items: string[] = [(b ?? n)![1]!]
      let j = i + 1
      for (; j < lines.length; j++) {
        const l = lines[j]!
        const more = kind === 'ul' ? bullet.exec(l) : numbered.exec(l)
        if (more) items.push(more[1]!)
        else if (l.trim() && /^\s{2,}/.test(l) && !fence.test(l)) items[items.length - 1] += ' ' + l.trim()
        else break
      }
      out.push({ t: kind, items })
      i = j - 1
      continue
    }
    para.push(line.trim())
  }
  flush()
  return out
}

export type Inline = { t: 'text'; text: string } | { t: 'code'; text: string } | { t: 'b'; text: string } | { t: 'link'; text: string; url: string }

const inlineRE = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|\[([^\]\n]+)\]\(([^)\s]+)\)/g

/** Reads the inline marks of a line. */
export function parseInline(s: string): Inline[] {
  const out: Inline[] = []
  let last = 0
  for (const m of s.matchAll(inlineRE)) {
    const at = m.index ?? 0
    if (at > last) out.push({ t: 'text', text: s.slice(last, at) })
    if (m[1] !== undefined) out.push({ t: 'code', text: m[1] })
    else if (m[2] !== undefined) out.push({ t: 'b', text: m[2] })
    else out.push({ t: 'link', text: m[3]!, url: m[4]! })
    last = at + m[0].length
  }
  if (last < s.length) out.push({ t: 'text', text: s.slice(last) })
  return out
}

export interface MarkdownProps {
  text: string
  /** Renders a link; null renders its label alone. */
  link: (url: string, label: string) => ReactNode | null
  /** Renders a code block. */
  code: (text: string, lang: string, open: boolean, key: number) => ReactNode
}

function inline(s: string, link: MarkdownProps['link']): ReactNode {
  return parseInline(s).map((x, i) => {
    switch (x.t) {
      case 'code':
        return (
          <code key={i} className="rk-mono rk-icode">
            {x.text}
          </code>
        )
      case 'b':
        return <strong key={i}>{x.text}</strong>
      case 'link': {
        const el = link(x.url, x.text)
        return <Fragment key={i}>{el ?? x.text}</Fragment>
      }
      default:
        return <Fragment key={i}>{x.text}</Fragment>
    }
  })
}

/** An answer, rendered. */
export function Markdown({ text, link, code }: MarkdownProps) {
  return (
    <>
      {parseBlocks(text).map((b, i) => {
        switch (b.t) {
          case 'p':
            return <p key={i}>{inline(b.text, link)}</p>
          case 'h':
            return (
              <p key={i}>
                <strong>{inline(b.text, link)}</strong>
              </p>
            )
          case 'ul':
            return (
              <ul key={i}>
                {b.items.map((it, j) => (
                  <li key={j}>{inline(it, link)}</li>
                ))}
              </ul>
            )
          case 'ol':
            return (
              <ol key={i} className="rk-ol">
                {b.items.map((it, j) => (
                  <li key={j}>{inline(it, link)}</li>
                ))}
              </ol>
            )
          case 'code':
            return code(b.text, b.lang, b.open, i)
          case 'table':
            return (
              <div key={i} className="rk-mdtable-wrap">
                <table className="rk-mdtable">
                  <thead>
                    <tr>
                      {b.head.map((h, j) => (
                        <th key={j}>{inline(h, link)}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((r, j) => (
                      <tr key={j}>
                        {r.map((c, k) => (
                          <td key={k}>{inline(c, link)}</td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
        }
        return null
      })}
    </>
  )
}
