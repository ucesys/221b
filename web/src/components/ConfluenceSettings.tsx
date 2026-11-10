import { useEffect, useState } from 'react'
import { checkConfluence, fetchConfluence, saveConfluence, testConfluence, type ConfluenceSettings as Settings, type ConfluenceView } from '../api/knowledge'
import { fmtTs } from '../lib/format'
import { Select } from './Select'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const blank: Settings = { on: false, baseURL: '', flavor: 'cloud', email: '', space: '', root: '', wiki: false, wikiSpace: '', wikiRoot: '', checkAt: '' }

/**
 * Settings › Issues › Confluence (Phase 12, KN-13 to KN-18): where the
 * known issues are published, and Mycroft's approved wiki pages beside
 * them. The write token is apart from Mycroft's read-only ones, kept in a
 * Secret and never shown again. Reporters' names, report text,
 * namespaces, hostnames and unapproved agent text never reach Confluence.
 */
export function ConfluenceSettings({ open }: { open: boolean }) {
  const [view, setView] = useState<ConfluenceView | null>(null)
  const [draft, setDraft] = useState<Settings>(blank)
  const [token, setToken] = useState('')
  const [replace, setReplace] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    if (!open) return
    let stop = false
    fetchConfluence().then(
      (v) => {
        if (stop) return
        setView(v)
        setDraft({ ...blank, ...v.settings })
      },
      (e: unknown) => {
        if (!stop) setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [open])
  const put = (p: Partial<Settings>) => setDraft((d) => ({ ...d, ...p }))
  const act = async (fn: () => Promise<void>) => {
    setBusy(true)
    setError('')
    setNote('')
    try {
      await fn()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const field = (k: keyof Settings, label: string, placeholder?: string, width?: number) => (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, width: width ?? 'auto' }}>
      <span>{label}</span>
      <span className="rk-field">
        <input value={String(draft[k] ?? '')} placeholder={placeholder} onChange={(e) => put({ [k]: e.target.value } as Partial<Settings>)} aria-label={label} data-testid={`confluence-${k}`} />
      </span>
    </label>
  )
  if (!view) {
    return (
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Confluence" data-testid="confluence-settings">
        <h3 className="rk-h">Confluence</h3>
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      </section>
    )
  }
  const changed = JSON.stringify(draft) !== JSON.stringify({ ...blank, ...view.settings }) || token !== ''
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 10 }} aria-label="Confluence" data-testid="confluence-settings">
      <h3 className="rk-h">Confluence</h3>
      <p className="rk-fhint" style={{ margin: 0 }}>
        The known issues are published to one space: a Status page grouped by service, one page per known issue, and a resolved archive by month. Each page has a part the issue service rewrites and a Notes part people edit freely; a person’s edit inside the rewritten part is never overwritten, its owner is asked. Pages are never deleted. A page someone writes under the tree becomes a draft known issue.
      </p>
      <label className="rk-tcheck">
        <input type="checkbox" checked={draft.on} onChange={(e) => put({ on: e.target.checked })} data-testid="confluence-on" />
        Publish the known issues to Confluence
      </label>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <Select label="Confluence" value={draft.flavor} options={[['cloud', 'Confluence Cloud (email and API token)'], ['datacenter', 'Confluence Data Center (personal access token)']]} onChange={(x) => put({ flavor: x as Settings['flavor'] })} width={320} testId="confluence-flavor" />
        {field('baseURL', 'Address', 'https://example.atlassian.net', 280)}
        {draft.flavor === 'cloud' && field('email', 'Account email', 'publisher@example.com', 220)}
      </div>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
        {field('space', 'Known issues’ space key', 'STATUS', 160)}
        {field('root', 'Top page title', 'Known issues', 220)}
        {field('checkAt', 'Nightly check at (UTC)', '03:30', 140)}
      </div>
      <label className="rk-tcheck">
        <input type="checkbox" checked={!!draft.wiki} onChange={(e) => put({ wiki: e.target.checked })} data-testid="confluence-wiki" />
        Also publish Mycroft’s approved wiki pages, read-only, with a link back to where they are edited
      </label>
      {draft.wiki && (
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          {field('wikiSpace', 'Wiki’s space key', 'INFRA', 160)}
          {field('wikiRoot', 'Wiki’s top page title', 'Infrastructure wiki', 220)}
        </div>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <span style={{ fontSize: 12.5 }}>Write token</span>
        <span className="rk-fhint" data-testid="confluence-token-state">
          {view.token ? 'A write token is saved in the Secret <release>-knowledge; it is never shown again.' : 'No write token saved.'} Use a token that may write the space(s) and nothing else: it is apart from the read-only tokens Mycroft reads its sources with.
        </span>
        {replace || !view.token ? (
          <span className="rk-field" style={{ maxWidth: 360 }}>
            <input type="password" autoComplete="off" value={token} placeholder="API token or personal access token" onChange={(e) => setToken(e.target.value)} aria-label="Write token" data-testid="confluence-token" />
          </span>
        ) : (
          <button className="rk-link" style={{ alignSelf: 'flex-start' }} onClick={() => setReplace(true)}>
            Replace…
          </button>
        )}
      </div>
      <dl className="rk-kv" data-testid="confluence-state">
        <dt>Pages</dt>
        <dd>
          {view.pages} known issues{view.settings.wiki ? `, ${view.wikiPages} wiki pages` : ''}
          {view.lastWrite ? ` · last written ${fmtTs(view.lastWrite)}` : ''}
        </dd>
        <dt>Outbox</dt>
        <dd>
          {view.outbox === 0 ? 'Nothing waiting' : `${view.outbox} waiting${view.failing ? `, ${view.failing} being tried again` : ''}`}
          {view.lastError ? ` · ${view.lastError}` : ''}
        </dd>
        <dt>Nightly check</dt>
        <dd>
          {view.lastCheck ? `Last ${fmtTs(view.lastCheck)}` : 'Not run yet'}
          {view.nextCheck ? ` · next ${fmtTs(view.nextCheck)}` : ''}
        </dd>
        {view.conflicts > 0 && (
          <>
            <dt>Edits waiting</dt>
            <dd>
              {view.conflicts} in <a className="rk-link" href="#/cases/inbox">AIOps › Inbox</a>
            </dd>
          </>
        )}
      </dl>
      {view.checkReport && view.checkReport.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="confluence-report">
          {view.checkReport.slice(0, 20).map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      )}
      {error && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
      {note && (
        <span className="rk-fhint" role="status" data-testid="confluence-note">
          {note}
        </span>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button
          className="rk-btn"
          disabled={busy}
          data-testid="confluence-test"
          onClick={() =>
            void act(async () => {
              const r = await testConfluence(draft, token || undefined)
              if (r.ok) setNote(`Reached ${(r.spaces ?? []).join(', ')}.`)
              else setError(r.error ?? 'Confluence did not answer.')
            })
          }
        >
          Test
        </button>
        <button
          className="rk-btn"
          data-primary="true"
          disabled={busy || !changed}
          data-testid="confluence-save"
          onClick={() =>
            void act(async () => {
              const v = await saveConfluence(draft, token || undefined)
              setView(v)
              setDraft({ ...blank, ...v.settings })
              setToken('')
              setReplace(false)
              setNote('Saved. Every page is written at the next check.')
            })
          }
        >
          Save
        </button>
        <button
          className="rk-btn"
          disabled={busy || !view.settings.on}
          data-testid="confluence-check"
          onClick={() =>
            void act(async () => {
              await checkConfluence()
              setNote('The check runs now: every page is compared with its record.')
            })
          }
        >
          Check now
        </button>
      </div>
    </section>
  )
}
