// Settings › Email (EM-01 to EM-04): the SMTP server the console emails
// the people involved in a case through. The password is written, never
// read back: the page sees only whether one is set.
import { call, post } from './call'

export type MailSecurity = 'starttls' | 'tls' | 'none'
export type MailState = 'notConfigured' | 'notTested' | 'failed' | 'on'

/** A test email: who sent it, to what address, when, and how it went. */
export interface MailTest {
  at: string
  by: string
  to: string
  ok: boolean
  error?: string
}

export interface MailSettings {
  host: string
  port: number
  security: MailSecurity
  username: string
  from: string
  passwordSet: boolean
  /** The password is kept in a Secret; false, in memory until the console restarts. */
  inSecret: boolean
  state: MailState
  on: boolean
  /** The last test with the settings as they stand. */
  tested?: MailTest
  /** The last test, whatever the settings were then. */
  lastTest?: MailTest
  updated?: string
  updatedBy?: string
  /** The console's address the links start with: Settings › Watson's. */
  link: string
  /** How the last email went on this replica. */
  lastSent?: { at: string; error?: string }
  /** The SuperAdmin's own address: the test's default. */
  you?: string
}

/** The settings as the page saves them: a new password, the one kept, or none. */
export interface MailInput {
  host: string
  port: number
  security: MailSecurity
  username: string
  from: string
  password?: string
  clearPassword?: boolean
}

const base = '/api/v1/settings/email'

export const DEFAULT_PORTS: Record<MailSecurity, number> = { starttls: 587, tls: 465, none: 25 }

export const fetchMail = () => call<MailSettings>(base)
export const saveMail = (m: MailInput) => call<MailSettings>(base, { method: 'PUT', body: JSON.stringify(m) })
export const testMail = (to: string) => call<MailSettings>(base + '/test', post({ to }))
