// Trust this certificate (0.12.1, CR-15 to CR-19): a Test of an https://
// address whose certificate is not trusted shows it, and a SuperAdmin may
// pin exactly that certificate with the credential, the source, the
// repository or the wiki's store. There is no switch that skips
// verification.

/** A certificate an address presented, as a Test shows it. */
export interface PresentedCert {
  subject: string
  issuer: string
  notBefore: string
  notAfter: string
  /** SHA-256 fingerprint, upper-case hex pairs joined by colons. */
  sha256: string
  names?: string[]
  selfSigned?: boolean
  expired?: boolean
  /** Normal verification succeeded (system CAs, private CA, name, dates). */
  trusted: boolean
  /** It is the pinned certificate. */
  pinned?: boolean
  /** The pinned fingerprint, when the address presents another (the certificate changed). */
  pinnedWas?: string
  /** It fails only because its CA is not trusted: it may be pinned. */
  trustable: boolean
  problem?: string
}

/** A pinned certificate, as kept with a credential, source, repository or store. */
export interface CertPin {
  sha256: string
  subject?: string
  issuer?: string
  notAfter?: string
  by?: string
  at?: string
}

/** A pin and what keeps it: the source, store or repository itself, or a credential (named). */
export interface PinnedWith {
  pin: CertPin
  with: 'source' | 'store' | 'credential' | 'repository'
  credentialName?: string
  repository?: string
}

/** A certificate's state in a few words, for the Test's result. */
export function certHeadline(c: PresentedCert): string {
  if (c.pinned && c.expired) return `The pinned certificate expired on ${c.notAfter.slice(0, 10)}; it is still accepted, as pinned.`
  if (c.pinned) return 'The pinned certificate: accepted.'
  if (c.pinnedWas && c.trustable) return `The certificate changed (was ${c.pinnedWas}, now ${c.sha256}).`
  if (c.trustable) return c.selfSigned ? 'The certificate is self-signed: the console does not trust it.' : 'The certificate is signed by a CA the console does not trust.'
  return `The certificate is not trusted${c.problem ? `: ${c.problem}` : ''}. Trusting it is not offered: it fails for another reason than its CA.`
}
