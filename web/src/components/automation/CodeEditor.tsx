import { useEffect, useRef } from 'react'

const LINE: React.CSSProperties = {
  fontSize: 12.5,
  lineHeight: '19px',
}

const GUTTER: React.CSSProperties = {
  ...LINE,
  flex: 'none',
  margin: 0,
  padding: '8px 10px 8px 8px',
  minWidth: 38,
  boxSizing: 'border-box',
  textAlign: 'right',
  color: 'var(--fg3)',
  background: 'var(--sunken)',
  borderRight: '1px solid var(--line)',
  userSelect: 'none',
}

const numbers = (n: number) => Array.from({ length: n }, (_, i) => i + 1).join('\n')

/** A script's text to edit, with its line numbers beside it. */
export function CodeArea({
  value,
  onChange,
  testId,
  height = 460,
  readOnly,
  jump,
}: {
  value: string
  onChange: (text: string) => void
  testId?: string
  height?: number | string
  readOnly?: boolean
  /** Go to this line; a new object goes again. */
  jump?: { line: number } | null
}) {
  const gutter = useRef<HTMLPreElement>(null)
  const area = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    const ta = area.current
    if (!jump || !ta) return
    const lines = ta.value.split('\n')
    const n = Math.min(Math.max(jump.line, 1), lines.length)
    const start = lines.slice(0, n - 1).reduce((a, l) => a + l.length + 1, 0)
    ta.focus()
    ta.setSelectionRange(start, start + (lines[n - 1]?.length ?? 0))
    ta.scrollTop = Math.max(0, (n - 1) * 19 - ta.clientHeight / 3)
    if (gutter.current) gutter.current.scrollTop = ta.scrollTop
  }, [jump])
  return (
    <div
      style={{
        display: 'flex',
        height,
        border: '1px solid var(--line)',
        borderRadius: 8,
        overflow: 'hidden',
        background: 'var(--surface)',
      }}
    >
      <pre ref={gutter} className="rk-mono" aria-hidden="true" style={{ ...GUTTER, overflow: 'hidden' }} data-testid={testId && `${testId}-lines`}>
        {numbers(value.split('\n').length)}
      </pre>
      <textarea
        ref={area}
        readOnly={readOnly}
        className="rk-mono"
        aria-label="Script"
        value={value}
        wrap="off"
        spellCheck={false}
        onChange={(e) => onChange(e.target.value)}
        onScroll={(e) => {
          if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop
        }}
        style={{
          ...LINE,
          flex: '1 1 auto',
          minWidth: 0,
          margin: 0,
          padding: 8,
          border: 0,
          outline: 'none',
          resize: 'none',
          tabSize: 2,
          color: 'var(--fg)',
          background: 'transparent',
        }}
        data-testid={testId}
      />
    </div>
  )
}

/** A script's text to read, with its line numbers beside it. */
export function CodeView({ text, testId, maxHeight }: { text: string; testId?: string; maxHeight?: string }) {
  const lines = text.replace(/\n$/, '').split('\n').length
  return (
    <div
      style={{
        overflow: 'auto',
        maxHeight,
        borderRadius: 8,
        background: 'var(--sunken)',
      }}
    >
      <div style={{ display: 'flex', minWidth: 'max-content' }}>
        <pre className="rk-mono" aria-hidden="true" style={GUTTER}>
          {numbers(lines)}
        </pre>
        <pre className="rk-mono" style={{ ...LINE, margin: 0, padding: 8 }} data-testid={testId}>
          {text}
        </pre>
      </div>
    </div>
  )
}
