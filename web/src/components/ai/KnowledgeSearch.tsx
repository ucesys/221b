import { useState, type ReactNode } from 'react'
import { saveEmbeddings, type KnowledgeView } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { Select } from '../Select'

type Run = (fn: () => Promise<unknown>, msg?: string) => Promise<void>

/**
 * Administration › AI › Knowledge › Search (phase-13.1-ai, part B): the
 * index in the database (KN-53), the ways it searches (KN-54) and the
 * embedding model of its semantic search (KN-55).
 */
export function SearchCard({ v, run, busy }: { v: KnowledgeView; run: Run; busy: boolean }) {
  const ix = v.index
  if (!ix) return null
  return (
    <section aria-label="Search" style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="knowledge-search">
      <h3 className="rk-h">Search</h3>
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="knowledge-index-line">
        The agents’ <span className="rk-mono">ask_knowledge</span> and the wiki’s search read one index in the database: {ix.documents.toLocaleString()} pages and excerpts, {ix.passages.toLocaleString()} passages. {modesText(v)}
      </p>
      {!ix.fuzzy && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-no-trgm">
          The database has no <span className="rk-mono">pg_trgm</span>: typos are not matched, only spellings such as r740a and r740-a.
        </p>
      )}
      <Embeddings v={v} run={run} busy={busy} />
    </section>
  )
}

/** The ways the search runs now, in words. */
export function modesText(v: KnowledgeView): string {
  const ix = v.index
  if (!ix) return ''
  const e = v.settings.embeddings
  const semantic = !!(e?.provider && e?.model && ix.semantic)
  const fuzzy = ix.fuzzy ? 'fuzzy matching' : 'spellings matched'
  return semantic ? `It searches by keyword, ${fuzzy} and meaning, ranked together.` : `It searches by keyword and ${fuzzy}; semantic search is off.`
}

/** One row of the card, as Knowledge's other cards have them. */
export function Row({ label, hint, children, testId }: { label: string; hint?: string; children: ReactNode; testId?: string }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(120px, 160px) 1fr', gap: 14, padding: '12px 0', borderTop: '1px solid var(--line)' }} data-testid={testId}>
      <div style={{ fontSize: 12.5, fontWeight: 600 }}>
        {label}
        {hint && <div style={{ fontWeight: 400, color: 'var(--fg3)', marginTop: 3 }}>{hint}</div>}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0, fontSize: 12.5 }}>{children}</div>
    </div>
  )
}

function Embeddings({ v, run, busy }: { v: KnowledgeView; run: Run; busy: boolean }) {
  const ix = v.index!
  const e = v.settings.embeddings ?? {}
  const [provider, setProvider] = useState(e.provider ?? '')
  const [model, setModel] = useState(e.model ?? '')
  const changed = provider !== (e.provider ?? '') || (provider !== '' && model.trim() !== (e.model ?? ''))
  const on = !!(e.provider && e.model)
  return (
    <Row label="Embedding model" hint="For semantic search: an OpenAI-compatible provider’s /v1/embeddings, the A5000’s vLLM by default" testId="knowledge-embeddings">
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <Select
          label="Embedding provider"
          value={provider}
          onChange={(x) => setProvider(x)}
          testId="knowledge-embedding-provider"
          options={[['', 'None: keyword and fuzzy search'], ...ix.providers.map((p): [string, string] => [p.id, p.endpoint ? `${p.name} · ${p.endpoint}` : p.name])]}
        />
        {provider !== '' && (
          <span className="rk-field" style={{ width: 260 }}>
            <input value={model} onChange={(x) => setModel(x.target.value)} placeholder="Qwen/Qwen3-Embedding-0.6B" aria-label="Embedding model" data-testid="knowledge-embedding-model" />
          </span>
        )}
        <button
          className="rk-btn"
          disabled={busy || !changed || (provider !== '' && !model.trim())}
          onClick={() => void run(() => saveEmbeddings(provider ? { provider, model: model.trim() } : {}), provider ? 'Saved: every passage is embedded again, in the background.' : 'Semantic search is off.')}
          data-testid="knowledge-embedding-save"
        >
          Save
        </button>
      </div>
      {ix.providers.length === 0 && (
        <span className="rk-fhint" data-testid="knowledge-embedding-none">
          No OpenAI-compatible provider is set up in Administration › AI › Providers.
        </span>
      )}
      {on && (
        <span className="rk-fhint" data-testid="knowledge-embedding-state">
          {ix.semantic
            ? `${ix.embedded.toLocaleString()} of ${ix.passages.toLocaleString()} passages embedded with ${e.model}${ix.embeddedAt ? ` (last pass ${fmtTs(ix.embeddedAt)})` : ''}.`
            : 'The database has no pgvector: semantic search waits for it (docs/database.md, The extensions).'}
        </span>
      )}
      {on && ix.embedError && (
        <span className="rk-t" data-tone="err" style={{ fontSize: 12 }} data-testid="knowledge-embedding-error">
          {ix.embedError}
        </span>
      )}
    </Row>
  )
}
