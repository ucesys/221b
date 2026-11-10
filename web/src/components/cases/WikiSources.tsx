// AIOps › Mycroft › Wiki › Sources (Phase 12, wave 5, MY-03, MY-07): what
// Mycroft reads, each with how many items and how its last read went
// (never an address, an account or a credential: Settings › Knowledge has
// those, for a SuperAdmin), and what people added in Mycroft, each with
// when it was read and the pages that draw on it; an Admin or a SuperAdmin
// may remove one, which leaves the index (its pages stay, and grow stale).
// Since MY-11, Add is in Settings › Knowledge's Added in Mycroft card only:
// its button here, and the empty list, lead there.
import { useState } from 'react'
import { pageHref, removeAdded, SOURCE_KIND_LABEL, type AddedItem, type WikiState } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { adminHref } from '../../lib/router'
import { ConfirmDialog } from '../ConfirmDialog'
import { MYCROFT_TABLE } from './mycroftTable'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** Where a note, a web address or documents are added: Settings › Knowledge's Added in Mycroft card (MY-11). */
export const ADD_HREF = adminHref('settings', ['knowledge', 'added'])

export function WikiSources({ wiki, onChanged, onToast }: { wiki: WikiState | null; onChanged: () => void; onToast?: (msg: string) => void }) {
  if (!wiki) return <p className="rk-fhint">Loading…</p>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="wiki-sources">
      {/* The table fills the card edge to edge, as Mycroft's other tables do (build 14). */}
      <section className="rk-card" style={{ display: 'flex', flexDirection: 'column', gap: 4 }} aria-label="Sources">
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', padding: '12px 16px 0' }}>
          <h2 className="rk-h" style={{ fontSize: 14, margin: 0, flex: '1 1 auto' }}>
            What Mycroft reads
          </h2>
          <span className="rk-fhint">
            {wiki.pages} pages, {wiki.excerpts} sources’ excerpts{wiki.running ? ' · a sync runs now' : ''}
          </span>
          {wiki.maySetup && (
            <a className="rk-link" href={adminHref('settings', ['knowledge'])} style={{ fontSize: 12.5 }} data-testid="wiki-sources-settings">
              Change the sources in Settings › Knowledge
            </a>
          )}
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table className="tb-table" style={MYCROFT_TABLE} data-testid="wiki-sources-table">
            <thead>
              <tr>
                <th>Source</th>
                <th>Kind</th>
                <th style={{ textAlign: 'right' }}>Items</th>
                <th>Last read</th>
              </tr>
            </thead>
            <tbody>
              {wiki.sources.map((s) => (
                <tr key={s.id} data-testid="wiki-source" data-source={s.id}>
                  <td>{s.name}</td>
                  <td>{SOURCE_KIND_LABEL[s.kind] ?? s.kind}</td>
                  <td style={{ textAlign: 'right' }}>{s.items}</td>
                  <td>
                    {s.off ? (
                      <span className="rk-pill" data-tone="muted">
                        Off
                      </span>
                    ) : s.error ? (
                      <span className="tb-err" title={s.error}>
                        Failed: {s.error}
                      </span>
                    ) : s.lastRead ? (
                      fmtTs(s.lastRead)
                    ) : (
                      <span className="rk-fhint">not read yet</span>
                    )}
                    {s.reported ? (
                      <span className="rk-pill" data-tone="warn" style={{ marginLeft: 6 }} title="Items that tried to give instructions, which were not followed">
                        {s.reported} reported
                      </span>
                    ) : null}
                    {s.skipped ? (
                      <span className="rk-pill" data-tone="muted" style={{ marginLeft: 6 }} title={s.skippedWhy} data-testid="wiki-source-skipped">
                        {s.skipped} skipped
                      </span>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <section className="rk-card" style={{ padding: '12px 16px', display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Added in Mycroft" data-testid="wiki-added">
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <h2 className="rk-h" style={{ fontSize: 14, margin: 0, flex: '1 1 auto' }}>
            Added in Mycroft
          </h2>
          {/* Add is in Settings › Knowledge only (MY-11). */}
          {wiki.mayAdd && !!wiki.store && (
            <a className="rk-btn" data-size="sm" href={ADD_HREF} data-testid="wiki-added-add">
              Add…
            </a>
          )}
        </div>
        <AddedItems wiki={wiki} onChanged={onChanged} onToast={onToast} />
      </section>
    </div>
  )
}

/**
 * What people added in Mycroft (MY-07), each with when it was read and the
 * pages that draw on it; an Admin or a SuperAdmin removes one. Listed under
 * Mycroft › Wiki › Sources and in Settings › Knowledge's Added in Mycroft
 * card (MY-11).
 */
export function AddedItems({ wiki, onChanged, onToast, inSettings = false }: { wiki: WikiState; onChanged: () => void; onToast?: (msg: string) => void; inSettings?: boolean }) {
  const [removing, setRemoving] = useState<AddedItem | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const remove = async () => {
    if (!removing) return
    setBusy(true)
    setError('')
    try {
      await removeAdded(removing.id)
      onToast?.(`“${removing.title}” removed: the agents no longer quote it.`)
      setRemoving(null)
      onChanged()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      {wiki.added.length === 0 ? (
        <span className="rk-fhint" data-testid="wiki-added-empty">
          {inSettings ? (
            'Nothing added yet.'
          ) : wiki.mayAdd ? (
            <>
              Nothing added yet. Add a note, a web address or documents in{' '}
              <a className="rk-link" href={ADD_HREF}>
                Settings › Knowledge
              </a>
              .
            </>
          ) : (
            'Nothing added yet. Admins and SuperAdmins add notes, web addresses and documents in Settings › Knowledge.'
          )}
        </span>
      ) : (
        <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
          {wiki.added.map((it) => (
            <li key={it.id} style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap', fontSize: 12.5 }} data-testid="wiki-added-item">
              <span style={{ flex: '1 1 260px', minWidth: 0, overflowWrap: 'anywhere' }}>
                {it.url ? (
                  <a className="rk-link" href={it.url} target="_blank" rel="noreferrer">
                    {it.title}
                  </a>
                ) : (
                  <strong>{it.title}</strong>
                )}{' '}
                <span className="rk-fhint" data-testid="wiki-added-state">
                  {it.lastRead ? `read ${fmtTs(it.lastRead)}` : 'waiting to be read'}
                </span>
                {it.reported && (
                  <span className="rk-pill" data-tone="warn" style={{ marginLeft: 6 }} title={it.reported}>
                    Tried to give instructions
                  </span>
                )}
              </span>
              {it.pages && it.pages.length > 0 && (
                <span className="rk-fhint">
                  Pages:{' '}
                  {it.pages.map((p, i) => (
                    <span key={p}>
                      {i > 0 && ', '}
                      <a className="rk-link" href={pageHref(p)}>
                        {p.replace(/^pages\//, '')}
                      </a>
                    </span>
                  ))}
                </span>
              )}
              {wiki.mayAdd && (
                <button className="rk-btn" data-size="sm" onClick={() => setRemoving(it)} data-testid="wiki-added-remove">
                  Remove
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {removing && (
        <ConfirmDialog title={`Remove “${removing.title}”?`} confirm="Remove" danger busy={busy} error={error} onCancel={() => setRemoving(null)} onConfirm={() => void remove()}>
          <p style={{ margin: 0 }}>It leaves the sources’ excerpts, so the agents no longer quote it. The wiki’s pages drawn from it stay, and grow stale.</p>
        </ConfirmDialog>
      )}
    </>
  )
}
