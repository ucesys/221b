// Settings › Email (EM-01 to EM-04, ADR-158): the SMTP server the console
// emails the people involved in a case through, when it is opened and when
// it changes. A SuperAdmin's. The password is written here and never shown
// again. Email is on only once a test with the settings as saved got
// through; changing any of them turns it off until the next test.
import { useCallback, useEffect, useState } from 'react'
import { DEFAULT_PORTS, fetchMail, saveMail, testMail, type MailInput, type MailSecurity, type MailSettings } from '../api/mail'
import { fmtTs } from '../lib/format'
import { adminHref } from '../lib/router'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const SECURITIES: { id: MailSecurity; label: string; hint: string }[] = [
  { id: 'starttls', label: 'STARTTLS', hint: 'Port 587: the connection is upgraded to TLS, which the server must offer.' },
  { id: 'tls', label: 'TLS', hint: 'Port 465: TLS from the start.' },
  { id: 'none', label: 'None', hint: 'Port 25, in the clear: only a relay inside your network that needs no sign-in.' },
]

/** The settings as the form edits them: a password typed, never the one kept. */
export interface Draft {
  host: string
  port: string
  security: MailSecurity
  username: string
  from: string
  password: string
  clearPassword: boolean
}

const draftOf = (m: MailSettings): Draft => ({ host: m.host, port: String(m.port || DEFAULT_PORTS[m.security]), security: m.security, username: m.username, from: m.from, password: '', clearPassword: false })

const same = (a: Draft, b: Draft) => JSON.stringify(a) === JSON.stringify(b)

/** What the status line says (EM-03): not configured, configured and not tested, tested when and by whom, or the last error. */
export function mailStatus(m: MailSettings): { text: string; tone: 'ok' | 'warn' | 'err' | 'muted' } {
  switch (m.state) {
    case 'notConfigured':
      return { text: 'Not configured: no email is sent.', tone: 'muted' }
    case 'on': {
      const t = m.tested
      return { text: `On: tested ${t ? `${fmtTs(t.at)} by ${t.by}, to ${t.to}` : ''}. The people involved in a case are emailed when it is opened and when it changes.`, tone: 'ok' }
    }
    case 'failed': {
      const t = m.tested
      return { text: `The last test${t ? `, ${fmtTs(t.at)} by ${t.by} to ${t.to},` : ''} failed: ${t?.error || 'no reason given'}. No email is sent until a test gets through.`, tone: 'err' }
    }
  }
  return { text: 'Configured, not tested: no email is sent until a test with these settings gets through.', tone: 'warn' }
}

/** The email settings as General edits them (CU-n): the page's one Save bar saves them with the rest, only when changed. */
export interface MailForm {
  saved: MailSettings | null
  draft: Draft | null
  /** The settings could not be read. */
  loadError: string
  changed: boolean
  set: (d: Partial<Draft>) => void
  undo: () => void
  /** Saves the settings as typed; says what was done, or throws what failed. */
  save: () => Promise<string>
  /** Takes the settings as the server has them now (a test email changes their state). */
  load: (m: MailSettings) => void
}

/** Reads the email settings and keeps the form's edits of them. */
export function useMailForm(): MailForm {
  const [saved, setSaved] = useState<MailSettings | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [loadError, setLoadError] = useState('')
  const load = useCallback((m: MailSettings) => {
    setSaved(m)
    setDraft(draftOf(m))
  }, [])
  useEffect(() => {
    let gone = false
    fetchMail().then(
      (m) => !gone && load(m),
      (e: unknown) => !gone && setLoadError(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [load])
  const changed = !!saved && !!draft && !same(draft, draftOf(saved))
  return {
    saved,
    draft,
    loadError,
    changed,
    set: (d) => setDraft((x) => (x ? { ...x, ...d } : x)),
    undo: () => saved && setDraft(draftOf(saved)),
    load,
    save: async () => {
      if (!draft) return ''
      const port = Number(draft.port)
      if (!(Number.isInteger(port) && port >= 1 && port <= 65535)) throw new Error('the port is 1 to 65535')
      const input: MailInput = { host: draft.host.trim(), port, security: draft.security, username: draft.username.trim(), from: draft.from.trim() }
      // The password goes only when a new one is typed.
      if (draft.password) input.password = draft.password
      if (draft.clearPassword) input.clearPassword = true
      load(await saveMail(input))
      return input.host ? 'Send a test email to turn email on.' : 'Email is off.'
    },
  }
}

/**
 * The Email section's fields (EM-01 to EM-04), edited in the form given;
 * General's Save bar saves them. Send test email tests what is saved, so
 * it waits while there are changes.
 */
export function EmailSettings({ mail, busy: saving }: { mail: MailForm; busy: boolean }) {
  const { saved, draft, set, changed } = mail
  const [to, setTo] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  if (!saved || !draft) {
    return (
      <p className={mail.loadError ? 'rk-t' : 'rk-fhint'} data-tone={mail.loadError ? 'err' : undefined} style={{ margin: 0 }} data-testid="email-settings">
        {mail.loadError || 'Loading…'}
      </p>
    )
  }
  const testTo = to ?? saved.you ?? ''
  const port = Number(draft.port)
  const portBad = !(Number.isInteger(port) && port >= 1 && port <= 65535)
  const status = mailStatus(saved)
  const test = async () => {
    setTesting(true)
    setError('')
    setNote('')
    try {
      const m = await testMail(testTo.trim())
      mail.load(m)
      setNote(m.on ? `A test email went to ${m.tested?.to ?? testTo}: email is on.` : '')
    } catch (e) {
      setError(errText(e))
    } finally {
      setTesting(false)
    }
  }
  const busy = saving || testing
  const sec = SECURITIES.find((s) => s.id === draft.security)!
  const field = { display: 'flex', flexDirection: 'column', gap: 4 } as const
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="email-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        The people involved in a case (who reported or opened it, its assignee, who follows it, wrote in its conversation or approved its fix) are emailed when it is opened and when its status, assignee or priority changes or someone writes
        in it; the changes within a minute are one email, and nobody is told of their own. An email names the case and what changed, with links to it and to every case the person is involved in: never a message’s text, a command or any
        output.
      </p>
      <p className="rk-t" data-tone={status.tone} role="status" style={{ margin: 0, fontSize: 13 }} data-testid="email-status">
        {status.text}
      </p>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
        <label style={{ ...field, flex: '1 1 260px' }}>
          <span className="rk-flabel">SMTP server</span>
          <input className="rk-field" value={draft.host} onChange={(e) => set({ host: e.target.value })} placeholder="smtp.example.com" autoComplete="off" disabled={busy} data-testid="email-host" />
        </label>
        <label style={{ ...field, width: 110 }}>
          <span className="rk-flabel">Port</span>
          <input className="rk-field" type="number" min={1} max={65535} value={draft.port} onChange={(e) => set({ port: e.target.value })} aria-invalid={portBad ? true : undefined} disabled={busy} data-testid="email-port" />
        </label>
      </div>
      <div style={field}>
        <span className="rk-flabel" id="email-security">
          Security
        </span>
        <div className="rk-seg" role="group" aria-labelledby="email-security" style={{ alignSelf: 'flex-start' }} data-testid="email-security">
          {SECURITIES.map((s) => (
            <button
              key={s.id}
              aria-pressed={draft.security === s.id}
              disabled={busy}
              onClick={() =>
                // The port follows the security while it is a usual one.
                set({ security: s.id, port: Object.values(DEFAULT_PORTS).includes(Number(draft.port)) ? String(DEFAULT_PORTS[s.id]) : draft.port })
              }
            >
              {s.label}
            </button>
          ))}
        </div>
        <span className="rk-fhint">{sec.hint}</span>
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
        <label style={{ ...field, flex: '1 1 220px' }}>
          <span className="rk-flabel">User name</span>
          <input className="rk-field" value={draft.username} onChange={(e) => set({ username: e.target.value })} autoComplete="off" disabled={busy || draft.security === 'none'} data-testid="email-username" />
        </label>
        <label style={{ ...field, flex: '1 1 220px' }}>
          <span className="rk-flabel">Password {saved.passwordSet && !draft.clearPassword ? '(set; type to replace it)' : ''}</span>
          <input
            className="rk-field"
            type="password"
            autoComplete="new-password"
            value={draft.password}
            onChange={(e) => set({ password: e.target.value, clearPassword: false })}
            placeholder={saved.passwordSet && !draft.clearPassword ? '••••••••' : ''}
            disabled={busy || draft.security === 'none'}
            data-testid="email-password"
          />
        </label>
        {saved.passwordSet && !draft.clearPassword && (
          <button className="rk-btn" data-size="sm" style={{ alignSelf: 'flex-end' }} onClick={() => set({ clearPassword: true, password: '' })} disabled={busy} data-testid="email-forget-password">
            Forget the password
          </button>
        )}
      </div>
      <span className="rk-fhint" style={{ marginTop: -8 }}>
        The password is kept {saved.inSecret ? 'in a Secret' : 'in memory, until the console restarts'}, never shown again, logged or backed up. It is sent only over TLS.
      </span>
      <label style={{ ...field, maxWidth: 420 }}>
        <span className="rk-flabel">From address</span>
        <input className="rk-field" type="email" value={draft.from} onChange={(e) => set({ from: e.target.value })} placeholder="221b@example.com" disabled={busy} data-testid="email-from" />
      </label>
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="email-link">
        {saved.link ? (
          <>
            The links start with <span className="rk-mono">{saved.link}</span>, the console’s address in{' '}
            <a className="rk-link" href={adminHref('settings', ['watson'])}>
              Settings › Watson
            </a>
            .
          </>
        ) : (
          <>
            The emails have no links until the console’s address is set in{' '}
            <a className="rk-link" href={adminHref('settings', ['watson'])}>
              Settings › Watson
            </a>{' '}
            (its notifications).
          </>
        )}{' '}
        With the chart’s network policy on, list the server’s address under networkPolicy.outboundCIDRs and its port under networkPolicy.outboundPorts.
      </p>
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Send a test email">
        <h3 className="rk-h">Test</h3>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, alignItems: 'flex-end' }}>
          <label style={{ ...field, flex: '1 1 260px', maxWidth: 360 }}>
            <span className="rk-flabel">Send a test email to</span>
            <input className="rk-field" type="email" value={testTo} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" disabled={busy} data-testid="email-test-to" />
          </label>
          <button
            className="rk-btn"
            disabled={busy || changed || saved.state === 'notConfigured' || !testTo.trim()}
            title={changed ? 'Save first' : saved.state === 'notConfigured' ? 'Save a server and a From address first' : 'Send a test email with the settings as saved'}
            onClick={() => void test()}
            data-testid="email-test"
          >
            Send test email
          </button>
        </div>
        {(error || note) && (
          <span className="rk-t" data-tone={error ? 'err' : 'ok'} role={error ? 'alert' : 'status'} style={{ fontSize: 12.5 }} data-testid="email-test-result">
            {error || note}
          </span>
        )}
        {saved.lastSent && (
          <span className={saved.lastSent.error ? 'tb-err' : 'rk-fhint'} style={{ fontSize: 12 }} data-testid="email-last-sent">
            {saved.lastSent.error ? `Last email, ${fmtTs(saved.lastSent.at)}: not sent, ${saved.lastSent.error}.` : `Last email sent ${fmtTs(saved.lastSent.at)}.`}
          </span>
        )}
      </section>
    </div>
  )
}
