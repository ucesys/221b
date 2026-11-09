import { useEffect, useState } from 'react'
import { fetchNotifications, saveNotifications, testChannel, type ChannelInput, type ChannelKind, type Notifications } from '../api/cards'
import { fmtTs } from '../lib/format'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KINDS: { id: ChannelKind; label: string; hint: string }[] = [
  { id: 'slack', label: 'Slack', hint: 'https://hooks.slack.com/services/…' },
  { id: 'teams', label: 'Microsoft Teams', hint: 'the incoming webhook of a Teams workflow, https://…' },
  { id: 'webhook', label: 'Webhook', hint: 'https://… (JSON POST)' },
]

/** A channel being edited: its new address, typed, never the one kept. */
interface Draft extends ChannelInput {
  urlSet: boolean
  lastAt?: string
  lastError?: string
}

const draftsOf = (n: Notifications): Draft[] => n.channels.map((c) => ({ id: c.id, kind: c.kind, name: c.name, on: c.on, url: '', urlSet: c.urlSet, lastAt: c.lastAt, lastError: c.lastError }))

/**
 * Where new agents' proposals are announced (Phase 12, ADR-102), for a
 * SuperAdmin in Settings › Watson: Slack, Teams or any webhook, each
 * message a summary and a link, never a secret or any output. An address
 * is written here and never shown again: whoever has it can post.
 */
export function NotificationChannels() {
  const [saved, setSaved] = useState<Notifications | null>(null)
  const [link, setLink] = useState('')
  const [drafts, setDrafts] = useState<Draft[]>([])
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const load = (n: Notifications) => {
    setSaved(n)
    setLink(n.link)
    setDrafts(draftsOf(n))
  }
  useEffect(() => {
    let gone = false
    fetchNotifications().then(
      (n) => !gone && load(n),
      (e: unknown) => !gone && setErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [])
  if (!saved) {
    return (
      <section aria-label="Notifications" data-testid="notify-channels">
        <h3 className="rk-h">Notifications</h3>
        <p className={err ? 'tb-err' : 'rk-fhint'}>{err || 'Loading…'}</p>
      </section>
    )
  }
  const set = (i: number, d: Partial<Draft>) => setDrafts(drafts.map((x, j) => (j === i ? { ...x, ...d } : x)))
  const changed = link !== saved.link || JSON.stringify(drafts.map(({ lastAt: _a, lastError: _e, ...d }) => d)) !== JSON.stringify(draftsOf(saved).map(({ lastAt: _a, lastError: _e, ...d }) => d))
  const save = async () => {
    setBusy(true)
    setErr('')
    setNote('')
    try {
      const n = await saveNotifications(
        link.trim(),
        drafts.map((d) => ({ id: d.id, kind: d.kind, name: d.name.trim(), on: d.on, url: d.url?.trim() || undefined, clearUrl: d.clearUrl || undefined })),
      )
      load(n)
      setNote('Saved.')
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const test = async (id: string, name: string) => {
    setErr('')
    setNote('')
    try {
      await testChannel(id)
      setNote(`A test message went to ${name}.`)
    } catch (e) {
      setErr(errText(e))
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Notifications" data-testid="notify-channels">
      <h3 className="rk-h">Notifications of proposed changes</h3>
      <p className="rk-fhint" style={{ margin: 0 }}>
        A proposed change that waits for approval is announced here as well as on the gear: its title, cluster, agent and a link, never its reasoning, evidence, commands or any output. Each address is kept {saved.inSecret ? 'in a Secret' : 'in memory, until the console restarts'} and never shown again. With the chart’s network policy on, the console reaches Slack and Teams over sherlock.egress; list other webhooks under networkPolicy.outboundCIDRs.
      </p>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4, maxWidth: 420 }}>
        <span className="rk-flabel">The console’s address, for the links</span>
        <input value={link} onChange={(e) => setLink(e.target.value)} placeholder="https://221b.example.com" aria-label="The console's address" data-testid="notify-link" />
      </label>
      {drafts.map((d, i) => {
        const kind = KINDS.find((k) => k.id === d.kind)!
        return (
          <div key={d.id ?? `new-${i}`} className="rk-card" style={{ padding: 10, display: 'flex', flexWrap: 'wrap', gap: 10, alignItems: 'flex-end' }} data-testid="notify-channel">
            <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4, width: 160 }}>
              <span className="rk-flabel">Kind</span>
              <select value={d.kind} onChange={(e) => set(i, { kind: e.target.value as ChannelKind })} aria-label="Kind">
                {KINDS.map((k) => (
                  <option key={k.id} value={k.id}>
                    {k.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4, width: 180 }}>
              <span className="rk-flabel">Name</span>
              <input value={d.name} maxLength={60} onChange={(e) => set(i, { name: e.target.value })} aria-label="Channel name" />
            </label>
            <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: '1 1 260px' }}>
              <span className="rk-flabel">Address {d.urlSet && !d.clearUrl ? '(set; type to replace it)' : ''}</span>
              <input
                type="password"
                autoComplete="off"
                value={d.url ?? ''}
                onChange={(e) => set(i, { url: e.target.value, clearUrl: false })}
                placeholder={d.urlSet && !d.clearUrl ? '••••••••' : kind.hint}
                aria-label="Address"
                data-testid="notify-url"
              />
            </label>
            <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
              <input type="checkbox" checked={d.on} onChange={(e) => set(i, { on: e.target.checked })} />
              <span>On</span>
            </label>
            {d.urlSet && !d.clearUrl && (
              <button className="rk-btn" data-size="sm" onClick={() => set(i, { clearUrl: true, url: '', on: false })}>
                Forget the address
              </button>
            )}
            {d.id && d.urlSet && (
              <button className="rk-btn" data-size="sm" onClick={() => void test(d.id!, d.name)} disabled={changed} title={changed ? 'Save first' : 'Send a test message'} data-testid="notify-test">
                Test
              </button>
            )}
            <button className="rk-btn" data-size="sm" onClick={() => setDrafts(drafts.filter((_, j) => j !== i))}>
              Remove
            </button>
            {(d.lastAt || d.lastError) && (
              <span style={{ flexBasis: '100%', fontSize: 12 }} className={d.lastError ? 'tb-err' : 'rk-fhint'}>
                {d.lastError ? `Last message, ${fmtTs(d.lastAt)}: not delivered, ${d.lastError}.` : `Last message delivered ${fmtTs(d.lastAt)}.`}
              </span>
            )}
          </div>
        )
      })}
      <div>
        <button className="rk-btn" onClick={() => setDrafts([...drafts, { kind: 'slack', name: '', on: true, url: '', urlSet: false }])} disabled={drafts.length >= 10} data-testid="notify-add">
          Add a channel
        </button>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <button className="rk-btn" data-primary="true" disabled={!changed || busy} onClick={() => void save()} data-testid="notify-save">
          Save the channels
        </button>
        {changed && (
          <button className="rk-link" onClick={() => load(saved)} disabled={busy} style={{ fontSize: 12.5 }}>
            Undo changes
          </button>
        )}
        {err ? (
          <span className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12.5 }} data-testid="notify-error">
            {err}
          </span>
        ) : note ? (
          <span className="rk-t" data-tone="ok" role="status" style={{ fontSize: 12.5 }}>
            {note}
          </span>
        ) : (
          changed && <span className="rk-fhint" style={{ fontSize: 12.5 }}>Not saved yet.</span>
        )}
      </div>
    </section>
  )
}
