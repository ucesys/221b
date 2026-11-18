// A run parameter's field with suggestions while it is typed (FU-03): a
// combobox, as the search field's (SearchBox.tsx) — the arrows move, Enter
// takes one, Escape closes the list and leaves the drawer open. A
// variable's suggestion shows its name, its values on the run's nodes and
// where it is set (FU-04); picking it is the caller's to handle. A value
// typed carries a badge at the field's right, "constant" (FU-06), which
// the field's accessible description reads too.
import { useEffect, useId, useRef, useState } from 'react'
import type { ParamSuggestion } from './paramSuggest'
import { ConstantBadge, EmptyChip } from './ValueTag'

interface Props {
  value: string
  onChange: (v: string) => void
  /** Takes a suggestion; without it, its value is typed in. */
  onPick?: (s: ParamSuggestion) => void
  /** What is offered for what was typed. */
  suggest: (typed: string) => ParamSuggestion[]
  /** Said in the list when nothing is offered: still listing, or why nothing can be. */
  status?: string
  label: string
  placeholder?: string
  disabled?: boolean
  /** Ties the field to the line that says what kind of parameter it is. */
  describedBy?: string
  /** What a value typed is, said inside the field at its right: "constant", "secret ref". */
  badge?: string
  /** What the box sends while it is empty, said as a chip (FU-07): an empty string, or nothing at all. */
  empty?: { kind: 'empty' | 'unset'; onClear?: () => void; clearLabel?: string }
  /** Takes the focus when it shows, as after a variable's chip is cleared. */
  focusOnMount?: boolean
  testId: string
}

export function ParamInput({ value, onChange, onPick, suggest, status, label, placeholder, disabled, describedBy, badge, empty, focusOnMount, testId }: Props) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const listId = useId()
  const input = useRef<HTMLInputElement>(null)
  const items = open && !disabled ? suggest(value) : []
  const said = open && !disabled && !items.length && status ? status : ''
  const shown = items.length > 0 || !!said
  const badgeId = `${listId}-badge`
  const tagged = !!badge && value !== ''
  const emptyId = `${listId}-empty`
  const blank = !!empty && value === ''

  // Only as it shows.
  const [focusFirst] = useState(focusOnMount)
  useEffect(() => {
    if (focusFirst) input.current?.focus()
  }, [focusFirst])

  // Typing starts the selection over.
  const [lastValue, setLastValue] = useState(value)
  if (value !== lastValue) {
    setLastValue(value)
    setActive(-1)
  }

  const pick = (s: ParamSuggestion) => {
    if (onPick) onPick(s)
    else onChange(s.value)
    setOpen(false)
    input.current?.focus()
  }

  return (
    <div className="rk-dd" style={{ position: 'relative', width: '100%' }}>
      <span className="rk-field" style={{ width: '100%' }}>
        <input
          ref={input}
          role="combobox"
          aria-label={label}
          aria-describedby={[describedBy, tagged ? badgeId : '', blank ? emptyId : ''].filter(Boolean).join(' ') || undefined}
          aria-expanded={items.length > 0}
          aria-controls={items.length ? listId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={items.length && active >= 0 ? `${listId}-${active}` : undefined}
          autoComplete="off"
          spellCheck={false}
          value={value}
          disabled={disabled}
          placeholder={placeholder}
          data-testid={testId}
          onChange={(e) => {
            onChange(e.target.value)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              if (!open) {
                setOpen(true)
                return
              }
              if (items.length) setActive((a) => (a + 1) % items.length)
            } else if (e.key === 'ArrowUp' && items.length) {
              e.preventDefault()
              setActive((a) => (a <= 0 ? items.length - 1 : a - 1))
            } else if (e.key === 'Enter' && items.length && active >= 0) {
              e.preventDefault()
              const s = items[active]
              if (s) pick(s)
            } else if (e.key === 'Escape' && shown) {
              e.stopPropagation()
              setOpen(false)
            }
          }}
        />
        {tagged && <ConstantBadge id={badgeId} label={badge} testId={`${testId}-badge`} />}
        {blank && empty && (
          <span id={emptyId} style={{ display: 'contents' }}>
            <EmptyChip kind={empty.kind} name={label} onClear={empty.onClear} clearLabel={empty.clearLabel} testId={`${testId}-empty`} />
          </span>
        )}
      </span>
      {items.length > 0 && (
        <div
          id={listId}
          role="listbox"
          aria-label={`Suggestions for ${label}`}
          className="rk-ddmenu"
          data-testid={`${testId}-suggestions`}
          style={{ left: 0, right: 0, minWidth: 0, maxHeight: 280, overflowY: 'auto', padding: 4, zIndex: 5 }}
          // Mouse down on an option must not blur the input before the click.
          onMouseDown={(e) => e.preventDefault()}
        >
          {items.map((s, i) => (
            <button
              key={s.value}
              type="button"
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              className="rk-ddopt"
              data-active={i === active}
              tabIndex={-1}
              onClick={() => pick(s)}
              style={{ width: '100%', gap: 8 }}
              data-testid={`${testId}-option`}
              data-bind={s.bind}
            >
              <span className="rk-mono" style={{ flex: '0 1 auto', overflow: 'hidden', textOverflow: 'ellipsis', fontSize: 12 }}>
                {s.label ?? s.value}
              </span>
              {s.detail !== undefined && (
                <span className="rk-mono" style={{ flex: '1 1 auto', overflow: 'hidden', textOverflow: 'ellipsis', fontSize: 11.5, color: 'var(--fg2)' }} data-testid={`${testId}-option-detail`}>
                  {s.detail}
                </span>
              )}
              <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--fg3)', whiteSpace: 'nowrap' }}>{s.note}</span>
            </button>
          ))}
        </div>
      )}
      {said && (
        <div role="status" className="rk-ddmenu" style={{ left: 0, right: 0, minWidth: 0, padding: '6px 10px', fontSize: 12, color: 'var(--fg3)', zIndex: 5 }} data-testid={`${testId}-status`}>
          {said}
        </div>
      )}
    </div>
  )
}
