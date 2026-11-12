import { useCallback, useEffect, useState } from 'react'
import { tokensText, type ListedModel, type ModelList } from '../../api/ai'
import { Select } from '../Select'

/** The Select's value for a model typed by hand. */
const OTHER = '\u0000other'

/** "qwen3.8-27b · 16,384 tokens". */
export function modelLabel(m: ListedModel): string {
  return m.contextWindow ? `${m.id} · ${tokensText(m.contextWindow)}` : m.id
}

interface Props {
  label: string
  hint?: string
  value: string
  /** A model chosen: from the list with its context window when the endpoint gives one. */
  onChange: (model: string, listed?: ListedModel) => void
  /** Lists the endpoint's models. */
  load: () => Promise<ModelList>
  /** Lists them as it appears (a saved provider), not only on the button. */
  auto?: boolean
  /** Where the list comes from (the kind and endpoint): a list from elsewhere is dropped. */
  from?: string
  /** Names suggested when nothing is listed. */
  suggestions?: string[]
  disabled?: boolean
  testId: string
}

/**
 * The model field (Phase 12, wave 5): a dropdown of the models the
 * provider's endpoint lists, each with its context window when the endpoint
 * says, and "Another model…" to type one; without a list, the field with
 * the usual suggestions, and why nothing was listed.
 */
export function ModelPicker({ label, hint, value, onChange, load, auto, from = '', suggestions = [], disabled, testId }: Props) {
  const [list, setList] = useState<ListedModel[] | null>(null)
  const [typing, setTyping] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(!!auto)
  // Another kind or endpoint: the list from the one before is dropped.
  const [listFrom, setListFrom] = useState(from)
  if (listFrom !== from) {
    setListFrom(from)
    setList(null)
    setTyping(false)
    setError('')
  }
  const take = useCallback((r: ModelList) => {
    const models = r.models ?? []
    setList(models)
    setError(r.error ?? (models.length ? '' : 'The endpoint lists no models: type the model’s name.'))
  }, [])
  const fail = (e: unknown) => setError(e instanceof Error ? e.message : String(e))
  const fetchList = async () => {
    setBusy(true)
    setError('')
    try {
      take(await load())
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }
  useEffect(() => {
    if (!auto) return
    let live = true
    load()
      .then(
        (r) => live && take(r),
        (e: unknown) => live && fail(e),
      )
      .finally(() => live && setBusy(false))
    return () => {
      live = false
    }
    // Only as it appears: the button lists again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const listed = list?.length ? list : null
  const inList = !!listed && listed.some((m) => m.id === value)
  const showInput = !listed || typing || (!!value && !inList)
  const listId = `${testId}-suggestions`
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, minWidth: 0, flex: '1 1 240px' }} data-testid={`${testId}-picker`}>
      <span style={{ fontWeight: 600 }}>{label}</span>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        {listed && (
          <Select
            label={`${label}: the endpoint's models`}
            value={showInput ? OTHER : value}
            options={[...(!value && !showInput ? [['', 'Choose a model…'] as [string, string]] : []), ...listed.map((m): [string, string] => [m.id, modelLabel(m)]), [OTHER, 'Another model…']]}
            onChange={(v) => {
              if (v === OTHER) {
                setTyping(true)
                return
              }
              setTyping(false)
              onChange(
                v,
                listed.find((m) => m.id === v),
              )
            }}
            disabled={disabled}
            testId={`${testId}-list`}
            width="min(100%, 360px)"
          />
        )}
        {showInput && (
          <span className="rk-field" style={{ flex: '1 1 200px', minWidth: 0 }}>
            <span className="rk-sr">{label}</span>
            <input spellCheck={false} list={listId} disabled={disabled} value={value} placeholder={listed ? 'The model’s name' : undefined} onChange={(e) => onChange(e.target.value.trim())} data-testid={testId} />
          </span>
        )}
        <datalist id={listId}>
          {(listed ? listed.map((m) => m.id) : suggestions).map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>
        <button type="button" className="rk-btn" disabled={busy || disabled} aria-busy={busy} onClick={() => void fetchList()} data-testid={`${testId}-load`}>
          {busy ? 'Listing…' : list ? 'List again' : 'List the endpoint’s models'}
        </button>
      </div>
      {(error || hint) && (
        <span className="rk-fhint" data-tone={error ? 'warn' : undefined} data-testid={`${testId}-hint`}>
          {error || hint}
        </span>
      )}
    </div>
  )
}
