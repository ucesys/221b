import { useEffect, useState } from 'react'
import { AUTO, offeredLabel, tieredOf, type ListedModel, type ModelList, type ProviderView } from '../../api/ai'
import { RefreshIcon } from '../Icons'
import { Select } from '../Select'
import { modelLabel } from './ModelPicker'

/** The Select's value for a model typed by hand. */
const OTHER = '\u0000other'
/** The Select's value while the list loads. */
const LOADING = '\u0000loading'
const WIDTH = 300

/**
 * The options of an agent's one Model dropdown (phase-13.1-ai, A9):
 * Default (its provider's default model), Auto when the provider has a
 * tiered model, then the models the provider offers (or, while none are
 * chosen, those its endpoint lists), and the current model first when it is
 * not among them, saying why.
 */
export function modelChoices(p: ProviderView | undefined, defaultText: string, value: string, listed: ListedModel[] | null): [string, string][] {
  const out: [string, string][] = [['', `Default (${defaultText})`]]
  if (tieredOf(p).length > 0) out.push([AUTO, 'Auto'])
  const all = p?.offersAll ?? !p?.offeredChosen
  const models: [string, string][] = all ? (listed ?? []).map((m): [string, string] => [m.id, modelLabel(m)]) : (p?.offers ?? []).map((o): [string, string] => [o.model, offeredLabel(o)])
  if (value && value !== AUTO && !models.some(([m]) => m === value)) out.push([value, `${value} (${all ? 'not listed by the provider' : 'no longer offered'})`])
  return [...out, ...models]
}

interface Props {
  provider?: ProviderView
  /** "claude-opus-5-5 on Anthropic". */
  defaultText: string
  /** '' the default, 'auto', or a model. */
  value: string
  onChange: (model: string, listed?: ListedModel) => void
  /** Lists the provider's models: wanted only while it offers every model it lists. */
  load: () => Promise<ModelList>
  testId: string
}

/**
 * An agent's model as one dropdown (phase-13.1-ai, A9), in place of the
 * checkboxes "Use the default model instead" and "Auto: …". A provider whose
 * offered models a SuperAdmin chose gives them; one with none chosen yet (an
 * upgrade) offers every model its endpoint lists, fetched as the field
 * appears and when the provider changes, and a model is typed only when the
 * list cannot be had (FU-01).
 */
export function ModelChoice({ provider, defaultText, value, onChange, load, testId }: Props) {
  const all = provider?.offersAll ?? !provider?.offeredChosen
  const from = `${provider?.id ?? ''} ${all}`
  const [list, setList] = useState<ListedModel[] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(all)
  // "A model by name…" chosen, before a name is typed.
  const [byName, setByName] = useState(false)
  const [listFrom, setListFrom] = useState(from)
  if (listFrom !== from) {
    setListFrom(from)
    setList(null)
    setError('')
    setBusy(all)
  }
  const take = (r: ModelList) => {
    const models = r.models ?? []
    setList(models)
    setError(r.error ?? (models.length ? '' : 'The endpoint lists no models.'))
  }
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
    if (!all) return
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
    // As it appears, and when the provider changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [from])
  const listed = list?.length ? list : null
  const loading = all && busy && !listed
  // Every model listed, and none could be: a model is typed.
  const typing = all && !loading && !listed
  const fixed = !!value && value !== AUTO
  const options = modelChoices(provider, defaultText, value, listed)
  const shown: [string, string][] = loading ? [...options, [LOADING, 'Listing the provider’s models…']] : typing ? [...options.filter(([v]) => v === '' || v === AUTO), [OTHER, 'A model by name…']] : options
  const selected = typing && (fixed || byName) ? OTHER : value
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, minWidth: 0 }} data-testid={`${testId}-picker`} data-state={loading ? 'loading' : typing ? 'typed' : 'listed'}>
      <span style={{ fontWeight: 600 }}>Model</span>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <Select
          label="Model"
          value={selected}
          options={shown}
          onChange={(v) => {
            if (v === LOADING) return
            if (v === OTHER) {
              setByName(true)
              return
            }
            setByName(false)
            onChange(
              v,
              listed?.find((m) => m.id === v),
            )
          }}
          testId={`${testId}-list`}
          width={WIDTH}
        />
        {typing && selected === OTHER && (
          <span className="rk-field" style={{ width: 220 }}>
            <span className="rk-sr">The model’s name</span>
            <input spellCheck={false} value={fixed ? value : ''} placeholder="The model’s name" onChange={(e) => onChange(e.target.value.trim())} data-testid={testId} />
          </span>
        )}
        {all && (
          <button
            type="button"
            className="rk-btn"
            disabled={busy}
            aria-busy={busy}
            aria-label="List the provider’s models again"
            title={busy ? 'Listing the provider’s models…' : 'List the provider’s models again'}
            onClick={() => void fetchList()}
            style={{ width: 30, height: 30, padding: 0, justifyContent: 'center', flex: 'none' }}
            data-testid={`${testId}-load`}
          >
            <RefreshIcon />
          </button>
        )}
      </div>
      {all && error && (
        <span className="rk-fhint" data-tone="warn" role="alert" data-testid={`${testId}-hint`}>
          {`${error.replace(/\.?$/, '.')} Type the model’s name.`}
        </span>
      )}
    </div>
  )
}
