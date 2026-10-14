import { useId, useMemo, useRef, useState } from 'react'
import { applySuggestion, suggestions as suggest, type Suggestion, type Term } from '../lib/table'
import { SearchIcon } from './Icons'

interface Props<R> {
  value: string
  onChange: (q: string) => void
  placeholder: string
  /** The accessible name of the field. */
  label: string
  /** The rows the suggestions come from, and the terms of each. */
  rows: R[]
  terms: (r: R) => Term[]
  width?: number
  /** For tests: data-testid on the input. */
  testId?: string
}

const KIND_LABEL: Record<string, string> = {
  name: 'name',
  label: 'label',
  taint: 'taint',
  namespace: 'namespace',
  status: 'status',
  node: 'node',
  owner: 'owner',
  release: 'release',
  kind: 'kind',
  type: 'type',
  chart: 'chart',
  ip: 'IP',
  host: 'host',
  port: 'port',
  detail: 'detail',
}

/**
 * The search field every table has: it filters as you type and offers
 * what the rows contain — names, labels, taints, namespaces, statuses — as
 * suggestions under the field (a combobox: arrows move, Enter takes).
 */
export function SearchBox<R>({ value, onChange, placeholder, label, rows, terms, width = 260, testId }: Props<R>) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const [focused, setFocused] = useState(false)
  const listId = useId()
  const input = useRef<HTMLInputElement>(null)
  const items = useMemo(() => (focused && value.trim() ? suggest(rows, terms, value) : []), [focused, rows, terms, value])
  const shown = open && items.length > 0

  // Typing starts the selection over.
  const [lastValue, setLastValue] = useState(value)
  if (value !== lastValue) {
    setLastValue(value)
    setActive(-1)
  }

  const pick = (s: Suggestion) => {
    onChange(applySuggestion(value, s))
    setOpen(false)
    input.current?.focus()
  }

  return (
    <div className="rk-dd" style={{ position: 'relative', width, maxWidth: '100%' }}>
      <label className="rk-field" style={{ width: '100%' }}>
        <SearchIcon />
        <span className="rk-sr">{label}</span>
        <input
          ref={input}
          type="search"
          role="combobox"
          aria-expanded={shown}
          aria-controls={shown ? listId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={shown && active >= 0 ? `${listId}-${active}` : undefined}
          autoComplete="off"
          placeholder={placeholder}
          value={value}
          data-testid={testId}
          onChange={(e) => {
            onChange(e.target.value)
            setOpen(true)
          }}
          onFocus={() => {
            setFocused(true)
            setOpen(true)
          }}
          onBlur={() => {
            setFocused(false)
            setOpen(false)
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' && items.length) {
              e.preventDefault()
              setOpen(true)
              setActive((a) => (a + 1) % items.length)
            } else if (e.key === 'ArrowUp' && items.length) {
              e.preventDefault()
              setActive((a) => (a <= 0 ? items.length - 1 : a - 1))
            } else if (e.key === 'Enter' && shown && active >= 0) {
              e.preventDefault()
              const s = items[active]
              if (s) pick(s)
            } else if (e.key === 'Escape' && shown) {
              e.stopPropagation()
              setOpen(false)
            }
          }}
        />
      </label>
      {shown && (
        <div
          id={listId}
          role="listbox"
          aria-label="Suggestions"
          className="rk-ddmenu"
          data-testid="suggestions"
          style={{ left: 0, right: 0, minWidth: 0, maxHeight: 320, overflowY: 'auto', padding: 4 }}
          // Mouse down on an option must not blur the input before the click.
          onMouseDown={(e) => e.preventDefault()}
        >
          {items.map((s, i) => (
          <button
            key={s.kind + s.text}
            id={`${listId}-${i}`}
            role="option"
            aria-selected={i === active}
            className="rk-ddopt"
            data-active={i === active}
            tabIndex={-1}
            onClick={() => pick(s)}
            style={{ width: '100%', gap: 8 }}
          >
            <span className="rk-mono" style={{ flex: '1 1 auto', overflow: 'hidden', textOverflow: 'ellipsis', fontSize: 12 }}>
              {s.text}
            </span>
            <span style={{ fontSize: 11, color: 'var(--fg3)', whiteSpace: 'nowrap' }}>
              {KIND_LABEL[s.kind] ?? s.kind} · {s.count}
            </span>
          </button>
          ))}
        </div>
      )}
    </div>
  )
}
