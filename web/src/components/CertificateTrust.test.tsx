import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PresentedCert } from '../api/certificates'
import { CertificateResult, PinnedCertNote } from './CertificateTrust'

// --- Trust this certificate (0.12.1, CR-15 to CR-19) ---

const FP = Array(32).fill('AB').join(':')
const NEW = Array(32).fill('CD').join(':')
const untrusted: PresentedCert = {
  subject: 'CN=minio.lab',
  issuer: 'CN=Lab CA',
  notBefore: '2026-01-01T00:00:00Z',
  notAfter: '2027-01-01T00:00:00Z',
  sha256: FP,
  names: ['minio.lab'],
  trusted: false,
  trustable: true,
  problem: 'x509: certificate signed by unknown authority',
}

describe('CertificateResult', () => {
  afterEach(() => cleanup())

  it('shows the certificate and trusts it only after the fingerprint is confirmed', async () => {
    const onTrust = vi.fn(async () => {})
    render(<CertificateResult cert={untrusted} mayTrust onTrust={onTrust} />)
    expect(screen.getByTestId('cert-headline').textContent).toContain('a CA the console does not trust')
    expect(screen.getByTestId('cert-issuer').textContent).toBe('CN=Lab CA')
    expect(screen.getByTestId('cert-names').textContent).toBe('minio.lab')
    fireEvent.click(screen.getByTestId('cert-trust'))
    const dialog = await screen.findByTestId('confirm')
    expect(dialog.textContent).toContain(FP)
    expect(onTrust).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByTestId('confirm-ok'))
    await waitFor(() => expect(onTrust).toHaveBeenCalledWith(FP))
    expect(await screen.findByTestId('cert-trusted')).toBeTruthy()
    expect(screen.queryByTestId('cert-trust')).toBeNull()
  })

  it('says the server’s refusal', async () => {
    render(<CertificateResult cert={untrusted} mayTrust onTrust={async () => Promise.reject(new Error('minio.lab presents another certificate now'))} />)
    fireEvent.click(screen.getByTestId('cert-trust'))
    fireEvent.click(within(await screen.findByTestId('confirm')).getByTestId('confirm-ok'))
    await waitFor(() => expect(screen.getByTestId('confirm').textContent).toContain('presents another certificate now'))
  })

  it('offers no Trust to others, for a failure that is not the CA’s, or where it cannot be kept', () => {
    const { unmount } = render(<CertificateResult cert={untrusted} mayTrust={false} onTrust={async () => {}} />)
    expect(screen.queryByTestId('cert-trust')).toBeNull()
    unmount()
    const wrongName = { ...untrusted, issuer: 'CN=R3', trustable: false, problem: 'x509: certificate is valid for other.example, not minio.lab' }
    const second = render(<CertificateResult cert={wrongName} mayTrust onTrust={async () => {}} />)
    expect(screen.queryByTestId('cert-trust')).toBeNull()
    expect(screen.getByTestId('cert-headline').textContent).toContain('valid for other.example')
    second.unmount()
    render(<CertificateResult cert={untrusted} mayTrust unavailable="Save the source first." />)
    expect(screen.queryByTestId('cert-trust')).toBeNull()
    expect(screen.getByTestId('cert-trust-unavailable').textContent).toBe('Save the source first.')
  })

  it('says a pinned certificate changed, and offers the new one; warns of an expired pinned one', () => {
    const { unmount } = render(<CertificateResult cert={{ ...untrusted, sha256: NEW, pinnedWas: FP }} mayTrust onTrust={async () => {}} />)
    expect(screen.getByTestId('cert-headline').textContent).toBe(`The certificate changed (was ${FP}, now ${NEW}).`)
    expect(screen.getByTestId('cert-trust')).toBeTruthy()
    unmount()
    render(<CertificateResult cert={{ ...untrusted, pinned: true, expired: true }} mayTrust onTrust={async () => {}} />)
    expect(screen.getByTestId('cert-headline').textContent).toContain('expired on 2027-01-01; it is still accepted')
    expect(screen.queryByTestId('cert-trust')).toBeNull()
  })
})

describe('PinnedCertNote', () => {
  afterEach(() => cleanup())

  it('names the fingerprint and who keeps it, and forgets after confirming', async () => {
    const onForget = vi.fn(async () => {})
    render(<PinnedCertNote pinned={{ pin: { sha256: FP, by: 'ann' }, with: 'credential', credentialName: 'minio' }} mayForget onForget={onForget} />)
    const note = screen.getByTestId('cert-pinned-note')
    expect(note.textContent).toContain('with the credential minio')
    expect(screen.getByTestId('cert-pinned-sha256').textContent).toBe(`SHA-256 ${FP}`)
    fireEvent.click(screen.getByTestId('cert-forget'))
    expect(onForget).not.toHaveBeenCalled()
    fireEvent.click(within(await screen.findByTestId('confirm')).getByTestId('confirm-ok'))
    await waitFor(() => expect(onForget).toHaveBeenCalled())
  })

  it('offers no Forget to others', () => {
    render(<PinnedCertNote pinned={{ pin: { sha256: FP } }} mayForget={false} onForget={async () => {}} />)
    expect(screen.queryByTestId('cert-forget')).toBeNull()
  })
})
