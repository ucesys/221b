import { useCallback, useEffect, useState } from 'react'
import { tokensText, type ListedModel, type ModelList } from '../../api/ai'
import { RefreshIcon } from '../Icons'
import { Select } from '../Select'

/** The Select's value for a model typed by hand. */
const OTHER = '\u0000other'
/** The Select's value while the list loads. */
const LOADING = '\u0000loading'
/** The model field's width in the list-only form, so that it lines up with the provider's. */
const LIST_ONLY_WIDTH = 260

/** "qwen3.8-27b · 16,384 tokens". */
export function modelLabel(m: ListedModel): string {
  return m.contextWindow ? `${m.id} · ${tokensText(m.contextWindow)}` : m.id
}

/**
 * The dropdown's options in the list-only form (FU-01): the listed models,
 * and the current value first when the list does not have it, so that a
 * model saved before stays what is chosen.
 */
export function listOnlyOptions(listed: ListedModel[], value: string): [string, string][] {
  const opts = listed.map((m): [string, string] => [m.id, modelLabel(m)])
  if (!value) return [['', 'Choose a model…'], ...opts]
  if (listed.some((m) => m.id === value)) return opts
  return [[value, `${value} (not listed by the provider)`], ...opts]
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
  /**
   * Only the listed models (FU-01, the agent drawer): the list is fetched as
   * the field appears and again whenever `from` changes, and a model is typed
   * only when the provider's list cannot be had.
   */
  listOnly?: boolean
  /** Names suggested when nothing is listed. */
  suggestions?: string[]
  disabled?: boolean
  testId: string
}

/**
 * The model field (Phase 12, wave 5): a dropdown of the models the
 * provider's endpoint lists, each with its context window when the endpoint
 * says, and "Another model…" to type one; without a list, the field with
 * the usual suggestions, and why nothing was listed. In its list-only form
 * (FU-01) the dropdown has the listed models alone, plus the current value
 * when the list lacks it.
 */
export function ModelPicker({ label, hint, value, onChange, load, auto, from = '', listOnly, suggestions = [], disabled, testId }: Props) {
  const [list, setList] = useState<ListedModel[] | null>(null)
  const [typing, setTyping] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(!!auto || !!listOnly)
  // Another kind or endpoint: the list from the one before is dropped.
  const [listFrom, setListFrom] = useState(from)
  if (listFrom !== from) {
    setListFrom(from)
    setList(null)
    setTyping(false)
    setError('')
    if (listOnly) setBusy(true)
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
    if (!auto && !listOnly) return
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
    // As it appears, and in the list-only form when the provider changes: the button lists again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listOnly ? from : ''])
  const listed = list?.length ? list : null
  const inList = !!listed && listed.some((m) => m.id === value)
  const loading = !!listOnly && busy && !listed
  const showInput = listOnly ? !listed && !loading : !listed || typing || (!!value && !inList)
  const listId = `${testId}-suggestions`
  const options: [string, string][] = !listed
    ? []
    : listOnly
      ? listOnlyOptions(listed, value)
      : [...(!value && !showInput ? [['', 'Choose a model…'] as [string, string]] : []), ...listed.map((m): [string, string] => [m.id, modelLabel(m)]), [OTHER, 'Another model…']]
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, minWidth: 0, flex: listOnly ? '0 0 auto' : '1 1 240px' }} data-testid={`${testId}-picker`} data-state={loading ? 'loading' : listed ? 'listed' : 'typed'}>
      <span style={{ fontWeight: 600 }}>{label}</span>
      <div style={{ display: 'flex', gap: 8, flexWrap: listOnly ? 'nowrap' : 'wrap', alignItems: 'center' }}>
        {loading && (
          <Select
            label={`${label}: the provider's models`}
            value={LOADING}
            options={[[LOADING, 'Listing the provider’s models…']]}
            onChange={() => {}}
            disabled
            testId={`${testId}-list`}
            width={listOnly ? LIST_ONLY_WIDTH : 'min(100%, 360px)'}
          />
        )}
        {listed && (
          <Select
            label={`${label}: the endpoint's models`}
            value={showInput ? OTHER : value}
            options={options}
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
            width={listOnly ? LIST_ONLY_WIDTH : 'min(100%, 360px)'}
          />
        )}
        {showInput && (
          <span className="rk-field" style={listOnly ? { width: LIST_ONLY_WIDTH } : { flex: '1 1 200px', minWidth: 0 }}>
            <span className="rk-sr">{label}</span>
            <input spellCheck={false} list={listId} disabled={disabled} value={value} placeholder={listed ? 'The model’s name' : undefined} onChange={(e) => onChange(e.target.value.trim())} data-testid={testId} />
          </span>
        )}
        <datalist id={listId}>
          {(listed ? listed.map((m) => m.id) : suggestions).map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>
        {/* FU-01: in the list-only form the list is fetched by itself, so listing again is a small refresh icon beside the field. */}
        {listOnly ? (
          <button
            type="button"
            className="rk-btn"
            disabled={busy || disabled}
            aria-busy={busy}
            aria-label="List the provider’s models again"
            title={busy ? 'Listing the provider’s models…' : 'List the provider’s models again'}
            onClick={() => void fetchList()}
            style={{ width: 30, height: 30, padding: 0, justifyContent: 'center', flex: 'none' }}
            data-testid={`${testId}-load`}
          >
            <RefreshIcon />
          </button>
        ) : (
          <button type="button" className="rk-btn" disabled={busy || disabled} aria-busy={busy} onClick={() => void fetchList()} data-testid={`${testId}-load`}>
            {busy ? 'Listing…' : list ? 'List again' : 'List the endpoint’s models'}
          </button>
        )}
      </div>
      {(error || hint) && (
        <span className="rk-fhint" data-tone={error ? 'warn' : undefined} role={error && listOnly ? 'alert' : undefined} data-testid={`${testId}-hint`}>
          {error ? (listOnly && !/type the model/i.test(error) ? `${error.replace(/\.?$/, '.')} Type the model’s name.` : error) : hint}
        </span>
      )}
    </div>
  )
}
