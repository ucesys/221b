import { useState } from 'react'
import { TIERS, offeredLabel, type ListedModel, type OfferedModel, type Tier } from '../../api/ai'
import { Select } from '../Select'

/** The Claude models an Anthropic provider offers Auto before a SuperAdmin chooses (A9): what Auto picked among before. */
export const CLAUDE_OFFERS: OfferedModel[] = [
  { model: 'claude-haiku-5-5', tier: 'light' },
  { model: 'claude-sonnet-5-5', tier: 'standard' },
  { model: 'claude-opus-5-5', tier: 'strong' },
]

const NAME = /^[A-Za-z0-9][A-Za-z0-9.:@_/+-]{0,127}$/

/** The models a provider offers in a few words: "gpt-5 · standard, gpt-5-mini · light", or every model it lists. */
export function offersText(offers: OfferedModel[] | undefined, all: boolean | undefined): string {
  if (all) {
    const tiered = (offers ?? []).filter((o) => o.tier)
    return tiered.length ? `Every model it lists; Auto picks among ${tiered.map(offeredLabel).join(', ')}` : 'Every model it lists'
  }
  return (offers ?? []).map(offeredLabel).join(', ') || '—'
}

interface Props {
  /** The models offered, in order. */
  value: OfferedModel[]
  /** A SuperAdmin chose them; before, the provider offers every model it lists. */
  chosen: boolean
  defaultModel?: string
  /** What the endpoint lists, when it was listed. */
  listed: ListedModel[] | null
  onChange: (offered: OfferedModel[]) => void
}

/**
 * The models a provider offers its agents (phase-13.1-ai, A9): a multiselect
 * of the models its endpoint lists, a model typed by hand for an endpoint
 * that lists none, each offered model with its tier for Auto (light,
 * standard, strong, or none: Auto does not pick it) and its place (the
 * first of a tier is the one Auto prefers). The default model is always
 * offered. Before a SuperAdmin chooses, every model it lists is offered;
 * any change chooses.
 */
export function OfferedModels({ value, chosen, defaultModel, listed, onChange }: Props) {
  const [name, setName] = useState('')
  const has = (m: string) => value.some((o) => o.model === m)
  const rows: { model: string; offered: boolean; tier: Tier; context?: number }[] = [
    ...value.map((o) => ({ model: o.model, offered: true, tier: o.tier ?? '' })),
    ...(defaultModel && !has(defaultModel) ? [{ model: defaultModel, offered: true, tier: '' as Tier }] : []),
    ...(listed ?? []).filter((m) => !has(m.id) && m.id !== defaultModel).map((m) => ({ model: m.id, offered: false, tier: '' as Tier })),
  ].map((r) => ({ ...r, context: listed?.find((m) => m.id === r.model)?.contextWindow }))
  const at = (m: string) => value.findIndex((o) => o.model === m)
  const set = (m: string, tier: Tier) => (has(m) ? onChange(value.map((o) => (o.model === m ? { ...o, tier } : o))) : onChange([...value, { model: m, tier }]))
  const toggle = (m: string, on: boolean) => onChange(on ? [...value, { model: m }] : value.filter((o) => o.model !== m))
  const up = (m: string) => {
    const i = at(m)
    if (i < 1) return
    const next = [...value]
    ;[next[i - 1], next[i]] = [next[i]!, next[i - 1]!]
    onChange(next)
  }
  const add = () => {
    const m = name.trim()
    if (!NAME.test(m) || has(m)) return
    onChange([...value, { model: m }])
    setName('')
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12.5 }} data-testid="provider-offered" data-chosen={chosen ? 'true' : 'false'}>
      <span style={{ fontWeight: 600 }}>Models offered to the agents</span>
      <span className="rk-fhint" data-testid="provider-offered-hint">
        {chosen
          ? 'An agent’s model, and Auto, choose among these only. Auto picks by tier: light, standard or strong; the first model of a tier is the one it prefers. A model with no tier is never picked by Auto.'
          : 'None chosen yet: the agents may use every model the provider lists, and Auto picks among the tiered models below. Change anything here to choose.'}
      </span>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {rows.map((r) => {
          const isDefault = r.model === defaultModel
          const unlisted = !!listed?.length && !listed.some((m) => m.id === r.model)
          return (
            <div key={r.model} style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }} data-testid="provider-offered-row" data-model={r.model} data-offered={r.offered ? 'true' : 'false'}>
              <label className="rk-tcheck" style={{ color: 'var(--fg)', flex: '1 1 240px', minWidth: 0 }}>
                <input type="checkbox" checked={r.offered} disabled={isDefault} onChange={(e) => toggle(r.model, e.target.checked)} data-testid="provider-offered-check" />
                <span className="rk-mono">{r.model}</span>
                {isDefault && <span className="rk-fhint"> · the default model, always offered</span>}
                {r.context ? <span className="rk-fhint"> · {r.context.toLocaleString('en')} tokens</span> : null}
                {unlisted && <span className="rk-fhint"> · not listed by the provider</span>}
              </label>
              <Select
                label={`Tier of ${r.model}`}
                value={r.tier}
                options={TIERS.map((t) => [t.id, t.label] as [string, string])}
                onChange={(v) => set(r.model, v as Tier)}
                disabled={!r.offered}
                testId="provider-offered-tier"
                width={130}
              />
              <button
                type="button"
                className="rk-btn"
                disabled={at(r.model) < 1}
                aria-label={`Move ${r.model} up`}
                title="Move up: the first of a tier is the one Auto prefers"
                onClick={() => up(r.model)}
                style={{ width: 30, height: 30, padding: 0, justifyContent: 'center', flex: 'none' }}
                data-testid="provider-offered-up"
              >
                ↑
              </button>
            </div>
          )
        })}
        {rows.length === 0 && <span className="rk-fhint">No model listed yet.</span>}
      </div>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <span className="rk-field" style={{ width: 240 }}>
          <span className="rk-sr">A model by name</span>
          <input
            spellCheck={false}
            value={name}
            placeholder="A model by name"
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                add()
              }
            }}
            data-testid="provider-offered-name"
          />
        </span>
        <button type="button" className="rk-btn" disabled={!NAME.test(name.trim()) || has(name.trim())} onClick={add} data-testid="provider-offered-add">
          Offer it
        </button>
        <span className="rk-fhint">For a server that lists none.</span>
      </div>
    </div>
  )
}
