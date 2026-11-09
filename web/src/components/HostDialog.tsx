import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { addHost, checkHost, fetchHosts, PairError, pairHost, parseHosts, parseLabels, scanHost, setupCommand, trustHost, type Credential, type KeyOffer } from '../api/hosts'
import { copyText } from '../lib/clipboard'
import { trapTab } from '../lib/focus'
import { layer } from '../lib/layer'
import { CopyIcon } from './Icons'

export type HostStep = 'details' | 'key' | 'pair' | 'done'

interface Props {
  cluster: string
  /** A host already registered, to confirm its key or pair it; none adds one. */
  host?: { name: string; address: string; port: number }
  start?: HostStep
  onClose: () => void
  onDone?: (message: string) => void
}

const STEPS: [HostStep, string][] = [
  ['details', 'Details'],
  ['key', 'Host key'],
  ['pair', 'Pair'],
]

/** A labelled field, as the fleet dialogs have them; bare holds no input box. */
function Field({ label, hint, children, bare, tall }: { label: string; hint?: string; children: ReactNode; bare?: boolean; tall?: boolean }) {
  const style = { display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)', minWidth: 0 } as const
  if (bare) {
    return (
      <div style={style}>
        <span>{label}</span>
        {children}
      </div>
    )
  }
  return (
    <label style={style}>
      {label}
      <span className="rk-field" style={{ width: '100%', ...(tall ? { height: 'auto', padding: '6px 10px' } : {}) }}>
        {children}
      </span>
      {hint && <span className="rk-fhint">{hint}</span>}
    </label>
  )
}

/** A command to copy, as the Toolbox shows them. */
function Command({ text, testId }: { text: string; testId: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div style={{ position: 'relative' }}>
      <pre className="rk-mono" data-testid={testId} style={{ margin: 0, padding: '8px 36px 8px 10px', maxHeight: 220, overflow: 'auto', fontSize: 11.5, background: 'var(--sunken)', border: '1px solid var(--line)', borderRadius: 6, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
        {text}
      </pre>
      <button
        className="rk-icon-btn"
        aria-label="Copy the command"
        title={copied ? 'Copied' : 'Copy'}
        style={{ position: 'absolute', top: 4, right: 4 }}
        onClick={async () => {
          if (await copyText(text)) {
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
          }
        }}
        data-testid={`${testId}-copy`}
      >
        <CopyIcon />
      </button>
    </div>
  )
}

/** One host of the dialog, and how it fares: its key read and trusted, then paired. */
interface Row {
  name: string
  address: string
  port: number
  offer?: KeyOffer
  reading?: boolean
  /** Its key is to be trusted: the SuperAdmin compared it. */
  tick?: boolean
  trusted?: boolean
  /** The login user, once paired. */
  paired?: string
  /** The name it was added under, its address, when it took its hostname on pairing. */
  was?: string
  error?: string
  steps?: string[]
}

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/**
 * Adds hosts outside Kubernetes (Phase 12), one or many at once (asked
 * November 5, 2026), or takes one further: their details, then each
 * host's key, read with no sign-in and trusted only once the SuperAdmin
 * compared the fingerprint on the host, then the pairing, with one
 * credential used once per host or with a setup command run by hand on
 * each. A credential leaves the page with the requests and its fields
 * are emptied at once, whatever the answers.
 */
export function HostDialog({ cluster, host, start, onClose, onDone }: Props) {
  const first: HostStep = start ?? (host ? 'key' : 'details')
  const [step, setStep] = useState<HostStep>(first)
  const [list, setList] = useState('')
  const [rows, setRows] = useState<Row[]>(host ? [{ ...host, reading: first === 'key', trusted: first === 'pair' }] : [])
  const [refused, setRefused] = useState<string[]>([])
  const [port, setPort] = useState('')
  const [defaults, setDefaults] = useState({ loginUser: 'root', port: 22 })
  const [labels, setLabels] = useState('')
  const [off, setOff] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [steps, setSteps] = useState<string[]>([])
  const [mode, setMode] = useState<'credential' | 'command'>('credential')
  const [user, setUser] = useState('')
  const [how, setHow] = useState<'password' | 'key'>('password')
  const [password, setPassword] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [sudoPassword, setSudoPassword] = useState('')
  const [command, setCommand] = useState('')
  const [done, setDone] = useState('')
  const box = useRef<HTMLElement>(null)
  const one = rows.length === 1
  const patch = (name: string, p: Partial<Row>) => setRows((rs) => rs.map((r) => (r.name === name ? { ...r, ...p } : r)))

  useEffect(() => {
    let gone = false
    fetchHosts(cluster).then(
      (l) => !gone && setDefaults({ loginUser: l.loginUser || 'root', port: l.port || 22 }),
      (e: unknown) => !gone && setOff(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [cluster])
  useEffect(() => {
    box.current?.querySelector<HTMLElement>('input, textarea, button[data-first]')?.focus()
  }, [step])

  const fail = (e: unknown) => {
    setError(errText(e))
    setSteps(e instanceof PairError ? e.steps : [])
  }
  const go = (next: HostStep) => {
    setError('')
    setSteps([])
    setStep(next)
  }

  // The keys are read as the step opens, and again on asking: each host
  // whose key is not trusted yet.
  const [reads, setReads] = useState(0)
  const names = rows.map((r) => r.name).join(' ')
  const unread = rows.filter((r) => r.reading).map((r) => r.name).join(' ')
  useEffect(() => {
    if (step !== 'key' || !unread) return
    let gone = false
    for (const name of unread.split(' ')) {
      scanHost(cluster, name)
        .then(
          (o) => !gone && patch(name, { offer: o, tick: true, error: undefined }),
          (e: unknown) => {
            if (gone) return
            patch(name, { error: errText(e), tick: false })
            if (!names.includes(' ')) fail(e)
          },
        )
        .finally(() => !gone && patch(name, { reading: false }))
    }
    return () => {
      gone = true
    }
  }, [step, reads, cluster, unread, names])
  const scan = () => {
    setError('')
    setRows((rs) => rs.map((r) => (r.trusted ? r : { ...r, offer: undefined, error: undefined, tick: false, reading: true })))
    setReads((n) => n + 1)
  }
  const ready = rows.filter((r) => r.trusted)
  const firstReady = ready[0]?.name ?? ''
  useEffect(() => {
    if (step !== 'pair' || mode !== 'command' || command || !firstReady) return
    let gone = false
    setupCommand(cluster, firstReady).then(
      (c) => !gone && setCommand(c.command),
      (e: unknown) => !gone && fail(e),
    )
    return () => {
      gone = true
    }
  }, [step, mode, command, cluster, firstReady])

  const credProblem = !user.trim() ? 'Give the account to sign in with.' : how === 'password' ? (!password ? 'Give its password.' : '') : !privateKey.trim() ? 'Paste its private key.' : ''
  const entries = parseHosts(list)
  const parsed = parseLabels(labels)
  const portN = port.trim() === '' ? defaults.port : Number(port)
  const detailsProblem =
    typeof entries === 'string' ? entries : !entries.length ? 'Give the host’s address, or several.' : !Number.isInteger(portN) || portN < 1 || portN > 65535 ? 'The SSH port is 1 to 65535.' : typeof parsed === 'string' ? parsed : mode === 'credential' ? credProblem : ''
  const register = async () => {
    if (detailsProblem || typeof parsed === 'string' || typeof entries === 'string') return
    setBusy(true)
    setError('')
    const added: Row[] = []
    const no: string[] = []
    for (const e of entries) {
      try {
        const h = await addHost(cluster, { name: e.name, address: e.address, port: portN, loginUser: defaults.loginUser, labels: parsed })
        added.push({ name: h.name, address: h.address, port: h.port, reading: true })
      } catch (err) {
        no.push(entries.length === 1 ? errText(err) : `${e.name}: ${errText(err)}`)
      }
    }
    setBusy(false)
    setRefused(added.length ? no : [])
    if (!added.length) {
      setError(no.join(' '))
      return
    }
    setRows(added)
    go('key')
  }
  const ticked = rows.filter((r) => r.offer && !r.trusted && (one || r.tick))
  // Hosts added here pair as soon as their keys are trusted, with the
  // credential given with them; one opened from its row pairs on the next step.
  const upFront = !host && mode === 'credential'
  const trust = async () => {
    if (!ticked.length) return
    setBusy(true)
    setError('')
    const now: Row[] = []
    for (const r of ticked) {
      try {
        await trustHost(cluster, r.name, r.offer!.fingerprint)
        patch(r.name, { trusted: true, error: undefined })
        now.push({ ...r, trusted: true })
      } catch (e) {
        patch(r.name, { error: errText(e) })
        if (one) fail(e)
      }
    }
    setBusy(false)
    if (!now.length) return
    go('pair')
    if (upFront && !credProblem) pairWith(now, [...ready, ...now])
  }

  const finish = (paired: Row[], as: string) => {
    const left = rows.length - paired.length
    setDone(
      one
        ? `${paired[0]!.name} is paired${paired[0]!.was ? `, named after its hostname (added as ${paired[0]!.was})` : ''}. Automation reaches it as a node, as ${as}.`
        : `${paired.length} of ${rows.length} hosts are paired. Automation reaches them as nodes, as ${as}.${left ? ` ${left} wait${left === 1 ? 's' : ''} in ${left === 1 ? 'its row' : 'their rows'} on the Nodes tab, to take further from there.` : ''}`,
    )
    go('done')
  }
  // Pairs, or checks, each host trusted and not paired yet; a failure
  // stays in its row, to try again.
  const each = async (todo: Row[], total: Row[], act: (r: Row) => Promise<{ name: string; loginUser: string; steps?: string[] }>) => {
    setBusy(true)
    setError('')
    setSteps([])
    let all = total.filter((r) => r.paired)
    let as = all[0]?.paired ?? ''
    for (const r of todo) {
      try {
        const res = await act(r)
        // A host added under its address takes its hostname as it pairs.
        const was = res.name !== r.name ? r.name : undefined
        patch(r.name, { name: res.name, was, paired: res.loginUser, steps: res.steps, error: undefined })
        all = [...all, { ...r, name: res.name, was, paired: res.loginUser }]
        as = res.loginUser
      } catch (e) {
        patch(r.name, { error: errText(e), steps: e instanceof PairError ? e.steps : [] })
        if (one) fail(e)
      }
    }
    setBusy(false)
    if (all.length === total.length) finish(all, as)
  }
  const unpaired = ready.filter((r) => !r.paired)
  // Pairs todo, of the trusted hosts total, with the credential in the fields.
  const pairWith = (todo: Row[], total: Row[]) => {
    if (credProblem) return
    const cred: Credential = { user: user.trim() }
    if (how === 'password') cred.password = password
    else {
      cred.privateKey = privateKey
      if (passphrase) cred.passphrase = passphrase
    }
    if (sudoPassword) cred.sudoPassword = sudoPassword
    // Out of the page at once: the requests carry it, nothing else.
    setPassword('')
    setPrivateKey('')
    setPassphrase('')
    setSudoPassword('')
    void each(todo, total, async (r) => {
      const res = await pairHost(cluster, r.name, { ...cred })
      return { name: res.host.name, loginUser: res.host.loginUser, steps: res.steps }
    })
  }
  const pair = () => pairWith(unpaired, ready)
  const check = () =>
    void each(unpaired, ready, async (r) => {
      const h = await checkHost(cluster, r.name)
      return { name: h.name, loginUser: h.loginUser }
    })
  const close = () => {
    if (step === 'done') onDone?.(done)
    else if (rows.some((r) => r.paired)) onDone?.(`${rows.filter((r) => r.paired).length} of ${rows.length} hosts are paired; the others wait in their rows on the Nodes tab.`)
    onClose()
  }

  // How to pair, and the credential's fields: with the details for hosts
  // added here, on the pair step for one from its row or to try again.
  const modeSwitch = (
    <div className="rk-seg" role="group" aria-label="How to pair">
      <button aria-pressed={mode === 'credential'} onClick={() => setMode('credential')} data-testid="host-mode-credential">
        Sign in once
      </button>
      <button aria-pressed={mode === 'command'} onClick={() => setMode('command')} data-testid="host-mode-command">
        Run this command yourself
      </button>
    </div>
  )
  const credFields = (
    <>
      <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>Give an account that may use sudo, or root{one ? '' : ', the same on every host'}. The console signs in with it once{one ? '' : ' on each host'}, runs the pairing as root and then logs in with its own certificate. The credential is not kept, logged or shown anywhere.</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: 12 }}>
        <Field label="Account">
          <input value={user} onChange={(e) => setUser(e.target.value)} data-testid="host-cred-user" autoComplete="off" spellCheck={false} />
        </Field>
        <Field label="Sign in with" bare>
          <div className="rk-seg" role="group" aria-label="Sign in with">
            <button aria-pressed={how === 'password'} onClick={() => setHow('password')} data-testid="host-cred-how-password">
              Password
            </button>
            <button aria-pressed={how === 'key'} onClick={() => setHow('key')} data-testid="host-cred-how-key">
              Private key
            </button>
          </div>
        </Field>
      </div>
      {how === 'password' ? (
        <Field label="Password">
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="host-cred-password" autoComplete="new-password" />
        </Field>
      ) : (
        <>
          <Field label="Private key" hint="As the file holds it, from -----BEGIN to -----END." tall>
            <textarea className="rk-mono" rows={5} value={privateKey} onChange={(e) => setPrivateKey(e.target.value)} data-testid="host-cred-key" autoComplete="off" spellCheck={false} style={{ width: '100%', border: 0, outline: 0, background: 'transparent', resize: 'vertical', color: 'var(--fg)', fontSize: 11.5 }} />
          </Field>
          <Field label="Passphrase, if it has one">
            <input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} data-testid="host-cred-passphrase" autoComplete="new-password" />
          </Field>
        </>
      )}
      <Field label="Sudo password, if not the same" hint="Given to sudo on its standard input, never on a command line. Not needed for root.">
        <input type="password" value={sudoPassword} onChange={(e) => setSudoPassword(e.target.value)} data-testid="host-cred-sudo" autoComplete="new-password" />
      </Field>
    </>
  )
  const title = host ? `Host ${host.name}` : 'Add hosts'
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
  const keyCommand = rows.find((r) => r.offer)?.offer?.command
  const reading = rows.some((r) => r.reading)
  // Each host's row, as the step has it.
  const rowList = (show: (r: Row) => ReactNode) => (
    <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Hosts">
      {rows.map((r) => (
        <li key={r.name} data-testid="host-row" data-host={r.name} style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 10px', fontSize: 12.5, padding: '6px 8px', border: '1px solid var(--line)', borderRadius: 6 }}>
          {show(r)}
        </li>
      ))}
    </ul>
  )
  const who = (r: Row) => (
    <>
      <span className="rk-mono" style={{ fontWeight: 600 }}>
        {r.name}
      </span>
      <span className="rk-mono" style={{ color: 'var(--fg3)' }}>
        {r.address}:{r.port}
      </span>
    </>
  )
  const rowError = (r: Row) =>
    r.error && (
      <span className="rk-t" data-tone="err" style={{ flexBasis: '100%' }} data-testid="host-row-error">
        {r.error}
        {r.steps && r.steps.length > 0 && (
          <ul style={{ margin: '4px 0 0', paddingLeft: 18, fontSize: 11.5, color: 'var(--fg2)' }} aria-label={`What the setup said on ${r.name}`}>
            {r.steps.map((s, i) => (
              <li key={i}>{s}</li>
            ))}
          </ul>
        )}
      </span>
    )
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 55, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Close" tabIndex={-1} onClick={close} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <section
        ref={box}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="rk-card"
        data-testid="host-dialog"
        data-step={step}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            close()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(620px, 100%)', maxHeight: '100%', overflow: 'auto', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)', overflowWrap: 'anywhere' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{title}</h2>
        {step !== 'done' && (
          <ol aria-label="Steps" style={{ display: 'flex', gap: 14, margin: 0, padding: 0, listStyle: 'none', fontSize: 12 }}>
            {STEPS.map(([id, label], i) => (
              <li key={id} aria-current={step === id ? 'step' : undefined} style={{ color: step === id ? 'var(--fg)' : 'var(--fg3)', fontWeight: step === id ? 600 : 400 }}>
                {i + 1}. {label}
              </li>
            ))}
          </ol>
        )}
        {off && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="host-off">
            {off}
          </p>
        )}

        {step === 'details' && (
          <>
            <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>Machines outside Kubernetes that the console reaches by SSH. Once paired, Automation runs scripts on them as on nodes: in fleets, in runs, with their facts. Their addresses must be ones the console's network policy lets it reach (networkPolicy.nodeCIDRs).</p>
            <Field label="Hosts" hint="IP addresses or DNS names, one or many, separated by spaces, tabs, commas or semicolons. Each is named after its address, as a node name; write name=address to name one otherwise. Not the name of a node." tall>
              <textarea className="rk-mono" rows={2} value={list} onChange={(e) => setList(e.target.value)} data-testid="host-list" autoComplete="off" spellCheck={false} style={{ width: '100%', border: 0, outline: 0, background: 'transparent', resize: 'vertical', color: 'var(--fg)', fontSize: 12.5 }} />
            </Field>
            {typeof entries !== 'string' && entries.length > 1 && (
              <p style={{ margin: 0, fontSize: 12, color: 'var(--fg2)' }} data-testid="host-list-count">
                {entries.length} hosts: {entries.map((e) => (e.name === e.address ? e.name : `${e.name} (${e.address})`)).join(', ')}.
              </p>
            )}
            <Field label="SSH port">
              <input value={port} placeholder={String(defaults.port)} inputMode="numeric" onChange={(e) => setPort(e.target.value)} data-testid="host-port" />
            </Field>
            <Field label="Labels (optional)" hint="key=value, by commas, for every host given. A host joins every fleet whose labels it has, and any list of nodes that names it.">
              <input value={labels} onChange={(e) => setLabels(e.target.value)} data-testid="host-labels" autoComplete="off" spellCheck={false} />
            </Field>
            <h3 className="rk-h" style={{ margin: '4px 0 0' }}>
              Pairing
            </h3>
            {modeSwitch}
            {mode === 'credential' ? (
              <>
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>Nothing is sent before you trust the host keys on the next step; then each host is paired with this account in turn.</p>
                {credFields}
              </>
            ) : (
              <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>Once the keys are trusted, the next step shows the command to run on each host as root.</p>
            )}
          </>
        )}

        {refused.length > 0 && step !== 'details' && (
          <p className="rk-t" data-tone="err" style={{ margin: 0, fontSize: 12 }} data-testid="host-refused">
            Not added: {refused.join('; ')}
          </p>
        )}

        {step === 'key' && (
          <>
            <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
              {one ? (
                <>
                  The console read the host key of <span className="rk-mono">{rows[0]!.name}</span> at <span className="rk-mono">{`${rows[0]!.address}:${rows[0]!.port}`}</span>, without signing in.
                </>
              ) : (
                <>The console read the host key of each host, without signing in. Untick any you do not expect.</>
              )}{' '}
              Trusting {one ? 'it pins it' : 'them pins them'}: from then on a host that shows another key is refused until a SuperAdmin trusts it again. Nothing is sent to a host before{upFront ? ', and then the account you gave pairs each in turn' : ''}.
            </p>
            {keyCommand && (
              <details style={{ fontSize: 12, color: 'var(--fg2)' }} data-testid="host-compare">
                <summary style={{ cursor: 'pointer' }}>Compare a key on its host first (optional)</summary>
                <p style={{ margin: '6px 0' }}>On the host, run this; it prints the fingerprint its row should show. Worth doing where someone could stand between the console and the hosts.</p>
                <Command text={keyCommand} testId="host-keygen" />
              </details>
            )}
            {rowList((r) => (
              <>
                {!one && <input type="checkbox" checked={!!r.tick && !r.trusted} disabled={!r.offer || r.trusted} onChange={() => patch(r.name, { tick: !r.tick })} aria-label={`Trust the key of ${r.name}`} data-testid="host-tick" style={{ alignSelf: 'center' }} />}
                {who(r)}
                {r.offer ? (
                  <span style={{ display: 'flex', alignItems: 'baseline', gap: 6, flexWrap: 'wrap' }}>
                    <code className="rk-mono" style={{ fontSize: 13, fontWeight: 600 }} data-testid="host-fingerprint">
                      {r.offer.fingerprint}
                    </code>
                    <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{r.trusted ? `${r.offer.keyType}, trusted` : r.offer.keyType}</span>
                  </span>
                ) : (
                  <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{r.reading ? 'Reading the host key…' : 'No key read.'}</span>
                )}
                {!one && rowError(r)}
              </>
            ))}
          </>
        )}

        {step === 'pair' && (
          <>
            {!one && (
              <>
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>Each host whose key you trusted is paired in turn; one that fails keeps its reason in its row, to try again with another account.</p>
                {rowList((r) => (
                  <>
                    {who(r)}
                    <span className="rk-t" data-tone={r.paired ? 'ok' : undefined} style={{ fontSize: 12, color: r.paired ? undefined : 'var(--fg3)' }} data-testid="host-row-state">
                      {r.paired ? `paired, as ${r.paired}${r.was ? `; named after its hostname, added as ${r.was}` : ''}` : r.trusted ? 'to pair' : 'key not trusted: left pending'}
                    </span>
                    {rowError(r)}
                  </>
                ))}
              </>
            )}
            {modeSwitch}
            {mode === 'credential' ? (
              (host || (unpaired.length > 0 && !busy)) && credFields
            ) : (
              <>
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>Run this on {one ? 'the host' : 'each host'} as root, or as an account that may use sudo. It makes the login user if missing, writes the console's public key to /etc/ssh/221b, sets TrustedUserCAKeys and AuthorizedPrincipalsFile, and reloads sshd. Then check the connection.</p>
                {command ? <Command text={command} testId="host-setup-command" /> : <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Making the command…</span>}
              </>
            )}
          </>
        )}

        {step === 'done' && (
          <>
            <p className="rk-t" data-tone="ok" style={{ margin: 0, fontSize: 13 }} data-testid="host-done">
              {done}
            </p>
            {!one &&
              rowList((r) => (
                <>
                  {who(r)}
                  <span style={{ fontSize: 12, color: 'var(--fg3)' }} data-testid="host-row-state">
                    {r.paired ? `paired, as ${r.paired}${r.was ? `; named after its hostname, added as ${r.was}` : ''}` : 'pending'}
                  </span>
                </>
              ))}
          </>
        )}

        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="host-error">
            {error}
          </p>
        )}
        {steps.length > 0 && (
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 11.5, color: 'var(--fg2)' }} aria-label="What the setup said" data-testid="host-steps">
            {steps.map((s, i) => (
              <li key={i}>{s}</li>
            ))}
          </ul>
        )}

        <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {step === 'details' && detailsProblem && list.trim() && <span style={{ fontSize: 12, color: 'var(--fg3)', marginRight: 'auto' }} data-testid="host-problem">{detailsProblem}</span>}
          <button className="rk-btn" onClick={close} disabled={busy} data-testid="host-close">
            {step === 'done' ? 'Close' : 'Cancel'}
          </button>
          {step === 'details' && (
            <button className="rk-btn" data-on="true" onClick={() => void register()} disabled={busy || !!off || !!detailsProblem} aria-busy={busy} data-testid="host-next">
              {typeof entries !== 'string' && entries.length > 1 ? `Add ${entries.length} hosts and read their keys` : 'Add and read its key'}
            </button>
          )}
          {step === 'key' && (
            <>
              <button className="rk-btn" onClick={scan} disabled={busy || reading} data-testid="host-rescan">
                Read again
              </button>
              <button className="rk-btn" data-on="true" onClick={() => void trust()} disabled={busy || reading || !ticked.length} aria-busy={busy} data-testid="host-trust">
                {`Trust ${one ? 'this key' : `${ticked.length} key${ticked.length === 1 ? '' : 's'}`}${upFront ? ' and pair' : ''}`}
              </button>
            </>
          )}
          {step === 'pair' && mode === 'credential' && (host || unpaired.length > 0) && (
            <button className="rk-btn" data-on="true" onClick={pair} disabled={busy || !!credProblem || !unpaired.length} title={credProblem || undefined} aria-busy={busy} data-testid="host-pair">
              {busy ? 'Pairing…' : one || unpaired.length === 1 ? 'Pair' : `Pair ${unpaired.length} hosts`}
            </button>
          )}
          {step === 'pair' && mode === 'command' && (
            <button className="rk-btn" data-on="true" onClick={check} disabled={busy || !command || !unpaired.length} aria-busy={busy} data-testid="host-check">
              {busy ? 'Checking…' : 'Check connection'}
            </button>
          )}
        </div>
      </section>
    </div>,
    layer(),
  )
}

/** The Nodes tab's button that adds a host, for a SuperAdmin. */
export function AddHostButton({ cluster, onToast }: { cluster: string; onToast?: (msg: string) => void }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button className="rk-btn" onClick={() => setOpen(true)} title="Add machines outside Kubernetes that Automation reaches by SSH: one or many" data-testid="host-add">
        Add host…
      </button>
      {open && <HostDialog cluster={cluster} onClose={() => setOpen(false)} onDone={(m) => m && onToast?.(m)} />}
    </>
  )
}
