import { useEffect, useState } from 'react'
import { createIssueToken, fetchIssueSettings, fetchIssueTokens, revokeIssueToken, saveIssueSettings, TOKEN_SCOPES, type IssueSettings as Settings, type IssueToken } from '../api/issues'
import { fmtTs } from '../lib/format'
import { ConfluenceSettings } from './ConfluenceSettings'
import { SaveBar } from './SaveBar'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/**
 * The issue service's settings (Phase 12, IS-29) and its API tokens
 * (IS-24), for SuperAdmins: the report page's name and links, the match
 * threshold, the timers, the webhook (the targets are Settings › SLA's), and the
 * tokens systems file reports with, each shown once when it is made.
 */
export function IssueSettings({ open }: { open: boolean }) {
  const [saved, setSaved] = useState<Settings | null>(null)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    if (!open) return
    let stop = false
    fetchIssueSettings().then(
      (s) => {
        if (stop) return
        setSaved(s)
        setDraft(s)
      },
      (e: unknown) => {
        if (!stop) setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [open])
  const save = async () => {
    if (!draft) return
    setBusy(true)
    setError('')
    setNote('')
    try {
      const s = await saveIssueSettings(draft)
      setSaved(s)
      setDraft(s)
      setNote('Saved.')
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const set = <K extends keyof Settings>(k: K, v: Settings[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d))
  const num = (k: keyof Settings, label: string, unit: string) => (
    <label className="rp-setrow">
      <span>{label}</span>
      <span className="rk-field" style={{ width: 110 }}>
        <input type="number" min={0} value={Number(draft?.[k] ?? 0)} onChange={(e) => set(k, Number(e.target.value) as never)} aria-label={label} />
      </span>
      <span className="rk-fhint">{unit}</span>
    </label>
  )
  const text = (k: 'company' | 'statusPage' | 'reportURL', label: string, hint: string) => (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span style={{ fontSize: 12.5 }}>{label}</span>
      <span className="rk-field">
        <input value={draft?.[k] ?? ''} onChange={(e) => set(k, e.target.value)} aria-label={label} />
      </span>
      <span className="rk-fhint">{hint}</span>
    </label>
  )
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 16 }} aria-label="Issues" data-testid="issue-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        People report problems at the report page, <a href={saved?.reportURL || '/report'}>{saved?.reportURL || '/report'}</a>. Each report is matched against the known issues; a confident match is linked and told the known issue's
        workaround and expected fix.
      </p>
      {!draft || !saved ? (
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      ) : (
        <>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 10 }} aria-label="The report page">
            <h3 className="rk-h">The report page</h3>
            {text('company', 'Company or IT name', 'In the page’s header, such as “Example Corp IT”.')}
            {text('statusPage', 'All known problems', 'A page that lists them, on Confluence say; empty hides the link.')}
            {text('reportURL', 'Report page address', 'Where other pages link to; empty is /report on this console.')}
          </section>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Matching and limits">
            <h3 className="rk-h">Matching and limits</h3>
            {num('matchThreshold', 'Link to a known issue at', '% confidence or more')}
            {num('ratePerHour', 'Reports one person may send', 'an hour')}
            {num('retentionDays', 'Keep a closed report’s text', 'days')}
            {num('duplicateHours', 'Alike reports on one service are similar within', 'hours')}
            {num('bumpP2At', 'Raise to P2 at', 'open reports on one known issue (0: never)')}
            {num('bumpP1At', 'Raise to P1 at', 'open reports on one known issue (0: never)')}
          </section>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Timers">
            <h3 className="rk-h">Timers</h3>
            {num('reopenDays', 'A closed report reopens within', 'days')}
            {num('fixedDays', 'A fix nobody answers closes after', 'days')}
            {num('needInfoDays', 'A question nobody answers closes after', 'days')}
          </section>
          {/* --- Cases replace reports (Phase 12, wave 5): the response targets are Settings › SLA's now. */}
          <p className="rk-fhint" style={{ margin: 0 }} data-testid="issue-settings-sla">
            The response and resolution targets per priority are in{' '}
            <a className="rk-link" href="#/admin/settings/sla">
              Settings › SLA
            </a>
            , for every case.
          </p>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Webhook">
            <h3 className="rk-h">Webhook</h3>
            <span className="rk-field">
              <input value={draft.webhook.url} placeholder="https://hooks.slack.com/services/…" onChange={(e) => set('webhook', { ...draft.webhook, url: e.target.value })} aria-label="Webhook address" />
            </span>
            <span className="rk-fhint">Told of every change as JSON with a text field Slack shows, carrying summaries only: never a report’s text. With the network policy on, its address must be allowed in the chart.</span>
          </section>
          <SaveBar changed={JSON.stringify(draft) !== JSON.stringify(saved)} busy={busy} onSave={() => void save()} onUndo={() => setDraft(saved)} error={error} saved={note} testId="issue-settings-save" />
        </>
      )}
      <Tokens open={open} />
      <ConfluenceSettings open={open} />
    </section>
  )
}

/** The API tokens systems file reports with (IS-24). */
function Tokens({ open }: { open: boolean }) {
  const [tokens, setTokens] = useState<IssueToken[] | null>(null)
  const [error, setError] = useState('')
  const [name, setName] = useState('')
  const [account, setAccount] = useState('')
  const [scopes, setScopes] = useState<string[]>(['reports:create'])
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (!open) return
    let stop = false
    fetchIssueTokens().then(
      (t) => {
        if (!stop) setTokens(t)
      },
      (e: unknown) => {
        if (!stop) setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [open])
  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      setTokens(await fetchIssueTokens())
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="API tokens" data-testid="issue-tokens">
      <h3 className="rk-h">API tokens</h3>
      <p className="rk-fhint" style={{ margin: 0 }}>
        Systems, such as monitoring or a help desk, file reports with a token at /api/issues/v1/intake/reports, as the account it names. A token is shown once, when it is made; only its hash is kept.
      </p>
      {tokens && tokens.length > 0 && (
        <ul className="rp-settokens">
          {tokens.map((t) => (
            <li key={t.id}>
              <span>
                <strong>{t.name}</strong> <span className="rk-mono rk-fhint">{t.id}</span> as <span className="rk-mono">system:{t.account}</span> · {t.scopes.join(', ')} · made {fmtTs(t.created)} by {t.createdBy.name || t.createdBy.id} ·{' '}
                {t.lastUsed ? `used ${fmtTs(t.lastUsed)}` : 'never used'}
              </span>
              <button type="button" className="rk-btn" disabled={busy} onClick={() => void run(() => revokeIssueToken(t.id).then(() => undefined))}>
                Revoke
              </button>
            </li>
          ))}
        </ul>
      )}
      {tokens?.length === 0 && <span className="rk-fhint">No tokens.</span>}
      <form
        className="rp-tokenform"
        onSubmit={(e) => {
          e.preventDefault()
          void run(async () => {
            const made = await createIssueToken(name.trim(), account.trim(), scopes)
            setSecret(made.secret)
            setName('')
            setAccount('')
          })
        }}
      >
        <span className="rk-field" style={{ width: 200 }}>
          <input value={name} placeholder="What it is for" onChange={(e) => setName(e.target.value)} aria-label="Token name" />
        </span>
        <span className="rk-field" style={{ width: 160 }}>
          <input value={account} placeholder="account" onChange={(e) => setAccount(e.target.value)} aria-label="Account" />
        </span>
        {TOKEN_SCOPES.map((s) => (
          <label key={s.id} className="rk-tcheck">
            <input type="checkbox" checked={scopes.includes(s.id)} onChange={(e) => setScopes((list) => (e.target.checked ? [...list, s.id] : list.filter((x) => x !== s.id)))} />
            {s.label}
          </label>
        ))}
        <button type="submit" className="rk-btn" disabled={busy || !name.trim() || !account.trim() || scopes.length === 0}>
          Make token
        </button>
      </form>
      {secret && (
        <div className="rk-card" style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 6 }} role="status" data-testid="issue-token-secret">
          <span style={{ fontSize: 12.5 }}>Copy it now: it is not shown again.</span>
          <code className="rk-mono" style={{ overflowWrap: 'anywhere' }}>
            {secret}
          </code>
          <button type="button" className="rk-link" style={{ alignSelf: 'flex-start' }} onClick={() => setSecret('')}>
            Done
          </button>
        </div>
      )}
      {error && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
    </section>
  )
}
