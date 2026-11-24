import { useState, type ReactNode } from 'react'
import { saveEmbeddings, saveSourceIndex, SOURCE_KIND_LABEL, TRUST_LABEL, type KnowledgeView, type SourceIndexView, type TrustLevel } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { Select } from '../Select'

type Run = (fn: () => Promise<unknown>, msg?: string) => Promise<void>

/**
 * Administration › AI › Knowledge › Search (phase-13.1-ai, part B): the
 * index in the database (KN-53), the ways it searches (KN-54), the
 * embedding model of its semantic search (KN-55) and what it holds of each
 * source (KN-58). A source's trust and what it is about (KN-56, KN-57) are
 * set on its row of the Sources table (SourceIndexCell), since 0.14.0.
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
      <Volume v={v} />
      <SourcesTable v={v} />
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
    <Row label="Embedding model" hint="For semantic search: an OpenAI-compatible provider’s /v1/embeddings. Type the model’s name as the endpoint knows it (its /v1/models)" testId="knowledge-embeddings">
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
            <input value={model} onChange={(x) => setModel(x.target.value)} placeholder="The model’s name, as its /v1/models lists it" aria-label="Embedding model" data-testid="knowledge-embedding-model" />
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

/** A size in bytes, in words. */
export function bytesText(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let x = n / 1024
  let i = 0
  while (x >= 1024 && i < units.length - 1) {
    x /= 1024
    i++
  }
  return `${x < 10 ? x.toFixed(1) : Math.round(x)} ${units[i]}`
}

/** The database's size against its volume's, and the warning at 80 % (KN-58). */
function Volume({ v }: { v: KnowledgeView }) {
  const ix = v.index!
  const share = ix.volumeBytes ? Math.round((ix.databaseBytes / ix.volumeBytes) * 100) : 0
  return (
    <>
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="knowledge-volume">
        {ix.volumeBytes
          ? `The database holds ${bytesText(ix.databaseBytes)} of its volume’s ${bytesText(ix.volumeBytes)} (${share} %). The index has no cap: the volume is its limit.`
          : `The database holds ${bytesText(ix.databaseBytes)}; its volume’s size is not known (database.external.volumeSize), so no warning is given.`}
      </p>
      {ix.full && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} role="alert" data-testid="knowledge-volume-full">
          The database holds {share} % of its volume. Grow the volume (database.bundled.storage.size), or turn off or remove the largest sources below.
        </p>
      )}
    </>
  )
}

/** What the index holds of each source (KN-58): its trust and what it is about are set on its row of the Sources table. */
function SourcesTable({ v }: { v: KnowledgeView }) {
  const ix = v.index!
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="tb-table" style={{ width: '100%' }} data-testid="knowledge-index-sources">
        <thead>
          <tr>
            <th scope="col">Source</th>
            <th scope="col">In the index</th>
          </tr>
        </thead>
        <tbody>
          {ix.sources.map((x) => (
            <tr key={x.id} data-testid="knowledge-index-source" data-source={x.id}>
              <td>
                <strong>{x.name}</strong>
                <div className="rk-fhint">{SOURCE_KIND_LABEL[x.kind]}</div>
              </td>
              <td data-testid="knowledge-index-size">
                {x.documents.toLocaleString()} {x.documents === 1 ? 'document' : 'documents'} · {x.passages.toLocaleString()} passages · {bytesText(x.bytes)}
              </td>
            </tr>
          ))}
          {ix.pageSize && ix.pageSize.documents > 0 && (
            <tr data-testid="knowledge-index-pages">
              <td>
                <strong>Approved pages</strong>
              </td>
              <td>
                {ix.pageSize.documents.toLocaleString()} pages · {bytesText(ix.pageSize.bytes)}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

/** A source's default trust, its kind's: the organisation's own high, web sites and buckets normal (KN-57). */
const kindTrust = (kind: string): TrustLevel => (({ confluence: 'high', git: 'high', upload: 'high', console: 'high', fixes: 'high' }) as Record<string, TrustLevel>)[kind] ?? 'normal'

/**
 * A source's trust and what it is about (KN-56, KN-57), on its row of Administration › AI › Knowledge's Sources:
 * the trust on top, the product, version and Only the versions the clusters run under it, saved together.
 * about false shows the trust alone (the sources of uploads), keeping what they are about as it is.
 */
export function SourceIndexCell({ x, clusters, run, busy, about = true }: { x: SourceIndexView; clusters: string[]; run: Run; busy: boolean; about?: boolean }) {
  const [trust, setTrust] = useState<'' | TrustLevel>(x.trustDefault ? '' : x.trust)
  const [product, setProduct] = useState(x.product ?? '')
  const [version, setVersion] = useState(x.productVersion ?? '')
  const [follows, setFollows] = useState(!!x.followsClusterVersions)
  const changed = trust !== (x.trustDefault ? '' : x.trust) || product.trim() !== (x.product ?? '') || version.trim() !== (x.productVersion ?? '') || follows !== !!x.followsClusterVersions
  const kindDefault = x.trustDefault ? x.trust : kindTrust(x.kind)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, alignItems: 'flex-start' }} data-testid="knowledge-index-cell" data-source={x.id}>
      <Select
        label={`Trust of ${x.name}`}
        value={trust}
        onChange={(t) => setTrust(t as '' | TrustLevel)}
        testId="knowledge-index-trust"
        options={[
          ['', `${TRUST_LABEL[kindDefault]} (its kind’s)`],
          ['high', 'High'],
          ['normal', 'Normal'],
          ['low', 'Low'],
        ]}
      />
      {about && (
        <>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }} title="The product and version its documents are about, where their front matter or path says nothing">
            <span className="rk-field" style={{ width: 110 }}>
              <input value={product} onChange={(e) => setProduct(e.target.value)} placeholder="product" aria-label={`Product of ${x.name}`} data-testid="knowledge-index-product" />
            </span>
            <span className="rk-field" style={{ width: 70 }}>
              <input value={version} onChange={(e) => setVersion(e.target.value)} placeholder="1.33" aria-label={`Version of ${x.name}`} data-testid="knowledge-index-version" />
            </span>
          </div>
          <label className="rk-fhint" style={{ display: 'flex', gap: 6, alignItems: 'center' }} title={clusters.length ? `The clusters run ${clusters.join(', ')}` : 'No cluster version is known yet'}>
            <input type="checkbox" checked={follows} onChange={(e) => setFollows(e.target.checked)} data-testid="knowledge-index-follows" />
            Only the versions the clusters run{clusters.length ? ` (${clusters.join(', ')})` : ''}
          </label>
        </>
      )}
      {changed && (
        <button
          className="rk-link"
          disabled={busy}
          onClick={() => void run(() => saveSourceIndex(x.id, { trust, product: product.trim(), productVersion: version.trim(), followsClusterVersions: follows }), 'Saved.')}
          data-testid="knowledge-index-save"
        >
          Save
        </button>
      )}
    </div>
  )
}
