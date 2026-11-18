import { useState } from 'react'
import { certHeadline, type CertPin, type PinnedWith, type PresentedCert } from '../api/certificates'
import { fmtTs } from '../lib/format'
import { ConfirmDialog } from './ConfirmDialog'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))
const day = (s?: string) => (s ? s.slice(0, 10) : '')

/**
 * What a Test found of an https:// address's certificate (CR-15): subject,
 * issuer, validity and SHA-256 fingerprint, and, for a SuperAdmin, Trust
 * this certificate when it fails only for its CA (CR-16). The server looks
 * at the address again and pins it only when it is this one. unavailable
 * says why Trust is not offered here (an unsaved source, say).
 */
export function CertificateResult({ cert, mayTrust, onTrust, unavailable }: { cert: PresentedCert; mayTrust: boolean; onTrust?: (sha256: string) => Promise<void>; unavailable?: string }) {
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)
  const offer = cert.trustable && !cert.pinned && !cert.trusted && mayTrust && !done
  const trust = async () => {
    if (!onTrust) return
    setBusy(true)
    setError('')
    try {
      await onTrust(cert.sha256)
      setDone(true)
      setAsking(false)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const row = (label: string, value: string, testid: string, mono = false) => (
    <>
      <dt style={{ color: 'var(--fg3)' }}>{label}</dt>
      <dd style={{ margin: 0, wordBreak: 'break-all' }} className={mono ? 'rk-mono' : undefined} data-testid={testid}>
        {value}
      </dd>
    </>
  )
  const tone = cert.pinned && !cert.expired ? 'ok' : cert.pinned ? 'warn' : 'err'
  return (
    <div className="rk-card" style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12.5 }} data-testid="cert-result">
      <span className="rk-t" data-tone={tone} data-testid="cert-headline">
        {certHeadline(cert)}
      </span>
      <dl style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '2px 10px', margin: 0 }}>
        {row('Subject', cert.subject, 'cert-subject')}
        {row('Issuer', cert.issuer + (cert.selfSigned ? ' (self-signed)' : ''), 'cert-issuer')}
        {row('Valid', `${day(cert.notBefore)} to ${day(cert.notAfter)}${cert.expired ? ' (expired)' : ''}`, 'cert-validity')}
        {cert.names?.length ? row('For', cert.names.join(', '), 'cert-names') : null}
        {row('SHA-256', cert.sha256, 'cert-sha256', true)}
      </dl>
      {done && (
        <span className="rk-t" data-tone="ok" data-testid="cert-trusted">
          Trusted: this certificate is accepted from now on, and no other in its place. Test again to check.
        </span>
      )}
      {offer && onTrust && (
        <button type="button" className="rk-btn" data-size="sm" style={{ alignSelf: 'flex-start' }} disabled={busy} onClick={() => setAsking(true)} data-testid="cert-trust">
          Trust this certificate
        </button>
      )}
      {offer && !onTrust && unavailable && (
        <span className="rk-fhint" data-testid="cert-trust-unavailable">
          {unavailable}
        </span>
      )}
      {cert.trustable && !cert.pinned && !mayTrust && <span className="rk-fhint">A SuperAdmin may trust it, or set the private CA.</span>}
      {error && !asking && (
        <span className="rk-t" data-tone="err" role="alert" data-testid="cert-trust-error">
          {error}
        </span>
      )}
      {asking && (
        <ConfirmDialog title="Trust this certificate?" confirm="Trust" busy={busy} error={error} onCancel={() => setAsking(false)} onConfirm={() => void trust()}>
          <p style={{ margin: 0 }}>
            From now on the console accepts exactly this certificate here, whatever signed it and whatever names it carries, and no other in its place: when it is renewed, connections fail until it is trusted again. Check the fingerprint
            with the server&apos;s owner first.
          </p>
          <p className="rk-mono" style={{ margin: '8px 0 0', wordBreak: 'break-all', fontSize: 12 }}>
            {cert.sha256}
          </p>
        </ConfirmDialog>
      )}
    </div>
  )
}

/** Who keeps a pin, in words. */
function withText(p: PinnedWith): string {
  switch (p.with) {
    case 'credential':
      return ` with the credential ${p.credentialName ?? ''}`
    case 'repository':
      return ` with the repository ${p.repository ?? ''}`
    default:
      return ''
  }
}

/**
 * The pinned certificate's note (CR-19): its fingerprint, who trusted it
 * when, and, for a SuperAdmin, Forget, after which the address is verified
 * normally again.
 */
export function PinnedCertNote({ pinned, onForget, mayForget }: { pinned: PinnedWith | { pin: CertPin; with?: undefined }; onForget?: () => Promise<void>; mayForget: boolean }) {
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const p = pinned.pin
  const forget = async () => {
    if (!onForget) return
    setBusy(true)
    setError('')
    try {
      await onForget()
      setAsking(false)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const [now] = useState(() => Date.now())
  const expired = !!p.notAfter && new Date(p.notAfter).getTime() < now
  return (
    <div className="rk-card" style={{ padding: '8px 12px', display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5 }} data-testid="cert-pinned-note">
      <span>
        <strong>Pinned certificate</strong>
        {pinned.with ? withText(pinned as PinnedWith) : ''}: accepted when normal verification fails, and no other.
      </span>
      <span className="rk-mono" style={{ wordBreak: 'break-all', fontSize: 12 }} data-testid="cert-pinned-sha256">
        SHA-256 {p.sha256}
      </span>
      <span className="rk-fhint">
        {p.subject ? `${p.subject}; ` : ''}
        {p.notAfter ? `valid until ${day(p.notAfter)}${expired ? ' (expired: still accepted)' : ''}; ` : ''}
        {p.by ? `trusted by ${p.by}` : 'trusted'}
        {p.at ? ` ${fmtTs(p.at)}` : ''}.
      </span>
      {mayForget && onForget && (
        <button type="button" className="rk-btn" data-size="sm" data-danger="true" style={{ alignSelf: 'flex-start' }} onClick={() => setAsking(true)} data-testid="cert-forget">
          Forget
        </button>
      )}
      {error && !asking && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
      {asking && (
        <ConfirmDialog title="Forget the pinned certificate?" confirm="Forget" danger busy={busy} error={error} onCancel={() => setAsking(false)} onConfirm={() => void forget()}>
          <p style={{ margin: 0 }}>The address is verified normally again: unless its certificate is signed by a CA the console trusts, connections fail until it is trusted again.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}
