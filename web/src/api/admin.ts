// The Administration area's data (RB-41 to RB-47): the four roles and who
// holds them, what each may do, the users 221B knows, and the changes a
// SuperAdmin makes to local users. Nothing here changes a cluster: roles
// and bindings come from the chart (RB-52), so there is no call that
// writes one. Hashes and credentials never come back (X-04).

import { useCallback, useEffect, useRef, useState } from 'react'
import { apiPath } from '../lib/router'
import { BACKGROUND, unauthorized } from './auth'

export type RoleId = 'superAdmin' | 'superViewer' | 'admin' | 'viewer'

/** The roles held in 221B, applied to every cluster (RB-05). */
export type SuperRole = 'superAdmin' | 'superViewer'

/** Where a name comes from; it decides the prefix Kubernetes sees (RB-15). */
export type Source = 'local' | 'ldap' | 'oidc'

/** The two definitions of Admin and SuperAdmin (RB-02). Viewer and SuperViewer have the read-only one only. */
export type Definition = 'full' | 'readOnly'

/** One subject a role is bound to, as the cluster has it. */
export interface Subject {
  kind: 'Group' | 'User' | 'ServiceAccount'
  /** The name without 221B's prefix (RB-15). */
  name: string
  /** Where it comes from; "other" for a binding 221B did not make. */
  source: Source | 'other'
  /** The namespaces it is bound in; empty for a role that is not bound per namespace. */
  namespaces: string[]
  /** Members of a local group. */
  members?: number
  /** True when 221B's chart made the binding; false for one made by hand (RB-42). */
  managed: boolean
  /** A bound user that does not exist (RB-42). */
  orphaned?: boolean
}

export interface RoleRow {
  id: RoleId
  name: string
  description: string
  /** "Every namespace, every cluster" or "Bound namespaces". */
  scope: string
  definition: Definition
  /** The ClusterRole's name: 221b-admin. */
  clusterRole: string
  /** Whether the role exists on this cluster; people see nothing until it does (RB-29). */
  present: boolean
  can: string[]
  cannot: string[]
  subjects: Subject[]
  /** Super roles are held in 221B, so these are the people and groups 221B lists, shown to SuperAdmin only. */
  holders: { groups: number; users: number }
}

export interface RolesResponse {
  roles: RoleRow[]
  /** False when this person may not list RoleBindings: the page then shows nothing (RB-41). */
  visible: boolean
  definitions: { admin: Definition; superAdmin: Definition }
  nodesRole: string
  /** Why the cluster has nothing to show, when it does not have the roles (RB-29). */
  message?: string
}

/** One rule of a role, as Kubernetes has it, flagged as the Access tab flags (RB-44). */
export interface PermRule {
  group: string
  resource: string
  verbs: string[]
  kind: 'read' | 'write'
  /** Where it applies for this role: the whole cluster, every namespace, the bound namespaces, or only the person's own (RB-07, RB-08). */
  scope: 'everywhere' | 'cluster' | 'all' | 'bound' | 'own'
  flags: string[]
  resourceNames?: string[]
  nonResource?: boolean
}

export interface PermRole {
  id: RoleId
  name: string
  clusterRole: string
  definition: Definition
  present: boolean
  rules: PermRule[]
}

export interface Drift {
  state: 'match' | 'differs' | 'unknown' | 'missing'
  roles: { id: RoleId; differences: string[] }[]
}

export interface PermissionsResponse {
  roles: PermRole[]
  drift: Drift
  /** rbac.enforce: with "strict", a cluster whose roles allow more than the shipped ones refuses every change (RB-36). */
  enforce: 'off' | 'strict'
  checkedAt?: string
  definitions: { admin: Definition; superAdmin: Definition }
}

export interface LocalGroup {
  name: string
  role: 'viewer' | 'admin'
  namespaces: string[]
  members: number
}

/** A role a person holds, and why. */
export interface Holding {
  role: RoleId
  /** The group it comes through; empty when it was given to the person. */
  via?: string
  namespaces?: string[]
}

export interface UserRow {
  /** Set when making the user a SuperAdmin waits for another SuperAdmin. */
  held?: string
  /** source:username, unique across sources (RB-15). */
  id: string
  username: string
  name?: string
  email?: string
  source: Source
  status: 'active' | 'disabled'
  /** Why a person is disabled: "Not in a listed group since the 12:04 sync". */
  statusNote?: string
  lastSignIn?: string
  holdings: Holding[]
  /** Groups the person is a member of, by name. */
  groups: string[]
  superRoles: SuperRole[]
  /** Defined in values.yaml: shown, never changed here (RB-46). */
  managed: boolean
  /** The name Kubernetes sees, with its prefix. */
  kubeName: string
  signedIn?: boolean
  self?: boolean
}

export interface LdapState {
  url: string
  state: 'connected' | 'error'
  message?: string
  lastSync?: string
  nextSync?: string
  people: number
  groups: number
  errors: string[]
  running: boolean
}

export interface UsersResponse {
  users: UserRow[]
  groups: LocalGroup[]
  ldap?: LdapState
  /** Whether local users can be created here: local mode, not LDAP (RB-28, RB-46). */
  canCreate: boolean
  /** Whether local users are kept after a restart (connections.store); false warns (RB-46). */
  persistent: boolean
}

export interface NewUser {
  username: string
  name: string
  email?: string
  password: string
  groups: string[]
}

export interface UserChange {
  disabled?: boolean
  name?: string
  email?: string
  /** A new password; it ends the person's sessions (RB-46). */
  password?: string
  /** The SuperAdmin's own password, which a new password needs (6.4). */
  currentPassword?: string
}

/** The server's refusal, with its message and status. */
export class AdminError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

/** What a refusal says, as an error for the page. */
async function failure(res: Response): Promise<AdminError> {
  if (unauthorized(res)) return new AdminError('Your session has ended; sign in again.', 401)
  let msg = `The server answered ${res.status}.`
  try {
    const b = (await res.json()) as { error?: string }
    if (b.error) msg = b.error
  } catch {
    // Not JSON: keep the status.
  }
  return new AdminError(msg, res.status)
}

async function request<T>(method: string, url: string, body?: unknown, background = false): Promise<T> {
  let res: Response
  try {
    res = await fetch(url, {
      method,
      headers: { ...(body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' }), ...(background ? BACKGROUND : {}) },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new AdminError('The server could not be reached.', 0)
  }
  if (!res.ok) throw await failure(res)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const enc = encodeURIComponent

export const fetchRoles = (cluster: string, background = false) => request<RolesResponse>('GET', apiPath('admin/roles', cluster), undefined, background)
export const fetchPermissions = (cluster: string, background = false) => request<PermissionsResponse>('GET', apiPath('admin/permissions', cluster), undefined, background)
export const fetchUsers = (background = false) => request<UsersResponse>('GET', '/api/v1/admin/users', undefined, background)

export const createUser = (u: NewUser) => request<UserRow>('POST', '/api/v1/admin/users', u)
export const changeUser = (username: string, c: UserChange) => request<UserRow>('PATCH', `/api/v1/admin/users/${enc(username)}`, c)
export const deleteUser = (username: string) => request<void>('DELETE', `/api/v1/admin/users/${enc(username)}`)
/** Adds a local user to groups and removes them from others: a change to 221B, not to a cluster (RB-51). */
export const changeGroups = (username: string, g: { add?: string[]; remove?: string[] }) => request<UserRow>('POST', `/api/v1/admin/users/${enc(username)}/groups`, g)
/** Gives or takes SuperAdmin and SuperViewer (RB-05, RB-51). */
export const setSuperRoles = (username: string, roles: SuperRole[]) => request<UserRow>('PUT', `/api/v1/admin/users/${enc(username)}/super`, { roles })
/** Ends every open session of a local user (RB-46). */
export const endSessions = (username: string) => request<void>('DELETE', `/api/v1/admin/users/${enc(username)}/sessions`)
/** Starts a directory sync in the background; one runs at a time (RB-25). */
export const refreshLdap = () => request<LdapState>('POST', '/api/v1/admin/ldap/refresh')

export interface Loaded<T> {
  data: T | null
  error: string
  /** The status of the refusal, so a page can say "SuperAdmin only" instead of a failure. */
  status: number
  loading: boolean
  reload: () => void
}

/** Loads one admin resource, again when its key changes or reload is called, and again every `every` ms while the page is open; load is told when it is the timer asking. */
export function useAdmin<T>(key: string, load: (background: boolean) => Promise<T>, every = 0): Loaded<T> {
  const [state, setState] = useState<{ key: string; data: T | null; error: string; status: number }>({ key, data: null, error: '', status: 0 })
  const [tick, setTick] = useState(0)
  const loader = useRef(load)
  useEffect(() => {
    loader.current = load
  })
  useEffect(() => {
    let gone = false
    const run = (background = false) => {
      loader.current(background).then(
        (data) => {
          if (!gone) setState({ key, data, error: '', status: 0 })
        },
        (e: unknown) => {
          if (gone) return
          const status = e instanceof AdminError ? e.status : 0
          setState((s) => ({ key, data: s.key === key ? s.data : null, error: e instanceof Error ? e.message : String(e), status }))
        },
      )
    }
    run()
    const t = every > 0 ? setInterval(() => run(true), every) : undefined
    return () => {
      gone = true
      if (t) clearInterval(t)
    }
  }, [key, tick, every])
  const reload = useCallback(() => setTick((t) => t + 1), [])
  const current = state.key === key
  return { data: current ? state.data : null, error: current ? state.error : '', status: current ? state.status : 0, loading: !current || (state.data === null && state.error === ''), reload }
}

/** One record of the audit log (Administration › Audit log, asked October 30, 2026). */
export interface AuditEvent {
  time: string
  action: string
  who: { subject?: string; email?: string; name?: string }
  cluster?: string
  target?: string
  detail?: string
  outcome: string
  from?: string
  /** When the backup this event came back from was taken; absent for one recorded here. */
  restored?: string
}

export interface AuditPage {
  events: AuditEvent[]
  /** How many events are kept, before the filter and the limit. */
  total: number
  /** Whether events are kept at all (they are, with the settings ConfigMap). */
  stored: boolean
  /** How many events never reached the webhook. */
  dropped: number
  /** Every action among the kept events, for the filter. */
  actions: string[]
}

/** Every event the console keeps: the server's most, above what it keeps (the newest 2,000). */
export const fetchAudit = () => request<AuditPage>('GET', '/api/v1/admin/audit?limit=5000')

/** The events with the words, each somewhere in them, of an action or its family and an outcome: as the server's filter. */
export function filterAudit(events: AuditEvent[], f: { q: string; action: string; outcome: string }): AuditEvent[] {
  const words = f.q.toLowerCase().split(/\s+/).filter(Boolean)
  return events.filter((e) => {
    if (f.action && e.action !== f.action && !e.action.startsWith(f.action + '.')) return false
    if (f.outcome && e.outcome !== f.outcome) return false
    const text = [e.action, e.who.name, e.who.email, e.who.subject, e.cluster, e.target, e.detail, e.outcome, e.from].join(' ').toLowerCase()
    return words.every((w) => text.includes(w))
  })
}

/** A backup file (Administration › Backup, asked October 30, 2026): what the console keeps of its own, each secret sealed with the install's backup key (ADR-137, asked November 9, 2026), or a placeholder (asked November 4, 2026). Since Phase 12 each key past the header is a part some package registered (ADR-80); the console's own are typed here. */
export interface BackupFile {
  format: string
  version: string
  taken: string
  by?: string
  leftOut: string[]
  /** How its secrets are sealed: the cipher and the key's fingerprint, never the key; absent when they are placeholders. */
  secrets?: { cipher: string; key: string }
  settings?: unknown
  users?: { username: string }[]
  connections?: { id: string; name: string; server: string; auth: string; local?: boolean; credential?: Record<string, string> }[]
  apps?: { cluster: string; namespace: string; name: string; repoToken?: string }[]
  sherlockKey?: string
  sshAuthority?: string
  toolboxSessions?: unknown[]
  sherlockSpend?: unknown
  sshPairings?: Record<string, unknown>
  sshHosts?: Record<string, unknown>
  audit?: unknown[]
  automation?: { scripts?: unknown[]; variables?: Record<string, Record<string, unknown>>; runProfiles?: unknown[]; groups?: unknown[] }
  /** The parts other packages register: jobs, AI agents, reports, … */
  [part: string]: unknown
}

export const BACKUP_FORMAT = '221b-backup/2'
/** The formats a restore takes: the first left the secrets out rather than holding placeholders. */
export const BACKUP_FORMATS = [BACKUP_FORMAT, '221b-backup/1']
/** What stands where a secret was, in a backup taken without a key or before ADR-137 (clusters.Placeholder). */
export const SECRET_PLACEHOLDER = '<secret: not in the backup>'
/** How a file encrypted on the console before it went to S3 starts (ADR-82). */
export const SEALED_MAGIC = '221b-backup-sealed/1 '

/** The console's own parts; other packages' parts are named by the server (fetchBackupParts). */
export type KnownPart = 'settings' | 'users' | 'connections' | 'apps' | 'toolboxSessions' | 'sherlockSpend' | 'sshHosts' | 'sshPairings' | 'audit' | 'automation'
export type RestorePart = KnownPart | (string & {})

export interface PartLabel {
  id: RestorePart
  label: string
  how: string
}

export const RESTORE_PARTS: PartLabel[] = [
  { id: 'settings', label: 'Settings', how: 'replace the current ones: refresh, time format, fleets, acknowledged alerts, Sherlock and Watson' },
  { id: 'users', label: 'Local users', how: 'are added, without a password, or updated: name, email, groups, super roles' },
  { id: 'connections', label: 'Cluster connections', how: 'missing here are added and wait for their credentials to be pasted again; a connection here keeps its own' },
  { id: 'apps', label: 'Helm applications', how: 'are added or replaced: source, values, parameters, commands; a repository token is pasted again. One this console would refuse is left out and named' },
  { id: 'toolboxSessions', label: 'Toolbox session records', how: 'are merged with those here' },
  { id: 'sherlockSpend', label: "Sherlock's spend", how: 'replaces the ledger here' },
  { id: 'sshHosts', label: 'Hosts outside Kubernetes', how: 'missing here are added with their pinned host keys; a host or node of the same name here keeps its place' },
  { id: 'sshPairings', label: 'SSH pairings', how: 'are added to those here, each checked first; they work with the same SSH authority Secret' },
  { id: 'audit', label: 'Audit log', how: 'is merged with the events here, each marked as restored' },
  { id: 'automation', label: 'Automation', how: 'scripts come back as drafts to submit for review again; run profiles and groups are added; fleet parameters apply, or on production wait for another SuperAdmin' },
]

/** A part as the server lists it: every registered one, the console's own and other packages'. */
export interface PartInfo {
  name: string
  label: string
  how?: string
  optional?: boolean
  restorable: boolean
}

/** The install's backup key as the page learns it (ADR-137): its fingerprint and Secret, never the key; no fingerprint when the install keeps none. */
export interface BackupKeyInfo {
  fingerprint?: string
  secret?: string
  error?: string
}

export const fetchBackupMeta = () => request<{ parts: PartInfo[]; key?: BackupKeyInfo }>('GET', '/api/v1/admin/backup/parts').then((r) => ({ parts: r.parts ?? [], key: r.key ?? {} }))
export const fetchBackupParts = () => fetchBackupMeta().then((r) => r.parts)

/** Whether a backup's secrets come back here: 'placeholders' (none in it), 'same' (this install's key opens them) or 'other' (sealed with another key, or this install keeps none). */
export function backupSealing(b: BackupFile, key: BackupKeyInfo | null): 'placeholders' | 'same' | 'other' {
  if (!b.secrets?.key) return 'placeholders'
  return key?.fingerprint && key.fingerprint === b.secrets.key ? 'same' : 'other'
}

/** A secret a restore did not bring back, and where to set it (ADR-137). */
export interface SecretToSet {
  part: string
  name: string
  where: string
}

/** The parts a restore offers: the console's own in their order, then the restorable parts the server names besides, then any other part the file holds, as a newer console wrote it. */
export function restoreParts(server: PartInfo[], b?: BackupFile): PartLabel[] {
  const out = [...RESTORE_PARTS]
  for (const p of server) if (p.restorable && !out.some((x) => x.id === p.name)) out.push({ id: p.name, label: p.label, how: p.how ?? '' })
  if (b) {
    for (const k of Object.keys(b)) {
      if (['format', 'version', 'taken', 'by', 'leftOut', 'secrets', 'sherlockKey', 'sshAuthority'].includes(k) || out.some((x) => x.id === k)) continue
      out.push({ id: k, label: k, how: 'is a part this console does not know; a restore names it and leaves it' })
    }
  }
  return out
}

export interface RestoreReport {
  restored: Partial<Record<RestorePart, number>>
  notes: string[]
  /** The secrets not brought back, each to set again where it says. */
  secrets?: SecretToSet[]
}

/** Takes a backup: the gzipped file as the server names it, and what it holds, for the page. optional takes the optional parts too (chat conversations). */
export async function fetchBackup(optional = false): Promise<{ bytes: Uint8Array; name: string; backup: BackupFile }> {
  let res: Response
  try {
    res = await fetch('/api/v1/admin/backup' + (optional ? '?optional=1' : ''), { headers: { Accept: 'application/gzip' } })
  } catch {
    throw new AdminError('The server could not be reached.', 0)
  }
  if (!res.ok) throw await failure(res)
  const bytes = new Uint8Array(await res.arrayBuffer())
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? '221b-backup.json.gz'
  return { bytes, name, backup: await readBackup(bytes) }
}

/** Whether bytes are a backup encrypted on the console (ADR-82), which only the server opens. */
export function isSealed(bytes: Uint8Array): boolean {
  return new TextDecoder().decode(bytes.slice(0, SEALED_MAGIC.length)) === SEALED_MAGIC
}

/** Has the server open an encrypted backup with the passphrase it keeps. */
async function openSealed(bytes: Uint8Array): Promise<BackupFile> {
  let res: Response
  try {
    res = await fetch('/api/v1/admin/backup/open', { method: 'POST', headers: { Accept: 'application/json', 'Content-Type': 'application/octet-stream' }, body: bytes as BodyInit })
  } catch {
    throw new AdminError('The server could not be reached.', 0)
  }
  if (!res.ok) throw await failure(res)
  return (await res.json()) as BackupFile
}

/** Reads a backup file: gzipped JSON as the console writes it, plain JSON as the first format was, or encrypted on the console, which the server opens. */
export async function readBackup(file: Blob | Uint8Array): Promise<BackupFile> {
  const bytes = file instanceof Uint8Array ? file : new Uint8Array(await file.arrayBuffer())
  if (isSealed(bytes)) return openSealed(bytes)
  let text: string
  if (bytes[0] === 0x1f && bytes[1] === 0x8b) {
    try {
      text = await new Response(new Response(bytes as BodyInit).body!.pipeThrough(new DecompressionStream('gzip'))).text()
    } catch {
      throw new Error('This file is gzipped but cannot be read: it is damaged or cut short.')
    }
  } else {
    text = new TextDecoder().decode(bytes)
  }
  let parsed: Partial<BackupFile> | null
  try {
    parsed = JSON.parse(text) as Partial<BackupFile> | null
  } catch {
    throw new Error('This is not a 221B backup: it is neither gzipped JSON nor JSON.')
  }
  if (!parsed || !BACKUP_FORMATS.includes(parsed.format ?? '')) throw new Error(`This is not a 221B backup: its format is ${JSON.stringify(parsed?.format ?? null)}, not ${BACKUP_FORMAT}.`)
  return parsed as BackupFile
}
/** Restores the parts picked; source says where the file came from, for the audit log. withoutSecrets is the yes to a restore whose secrets this install's key cannot open: without it the server refuses one with 409. */
export const restoreBackup = (backup: BackupFile, parts: RestorePart[], source?: string, withoutSecrets = false) =>
  request<RestoreReport>('POST', '/api/v1/admin/restore', { backup, parts: Object.fromEntries(parts.map((p) => [p, true])), ...(source ? { source } : {}), ...(withoutSecrets ? { withoutSecrets } : {}) })

/** How many of a part a backup holds, for the checkboxes. */
export function partCount(b: BackupFile, part: RestorePart): number {
  switch (part) {
    case 'settings':
      return b.settings ? 1 : 0
    case 'sherlockSpend':
      return b.sherlockSpend ? 1 : 0
    case 'sshPairings':
      return Object.keys(b.sshPairings ?? {}).length
    case 'sshHosts':
      return Object.keys(b.sshHosts ?? {}).length
    case 'connections':
      return (b.connections ?? []).filter((c) => !c.local).length
    case 'automation': {
      const a = b.automation
      if (!a) return 0
      const fleets = Object.values(a.variables ?? {}).reduce((n, f) => n + Object.keys(f).length, 0)
      return (a.scripts?.length ?? 0) + (a.runProfiles?.length ?? 0) + (a.groups?.length ?? 0) + fleets
    }
    default: {
      const v = b[part]
      if (Array.isArray(v)) return v.length
      if (v && typeof v === 'object') return Object.keys(v).length
      return v === undefined || v === null || v === '' ? 0 : 1
    }
  }
}

// ---------- Scheduled backups to S3 (Phase 12, ADR-81) ----------

/** A schedule as the jobs and the backups share it (package schedule). */
export interface ScheduleSpec {
  kind: 'daily' | 'weekdays' | 'weekly' | 'hours' | 'cron'
  /** HH:MM: when a daily, weekday or weekly schedule runs, and the first run each day of an every-N-hours one. */
  at?: string
  /** A weekly schedule's days: mon to sun. */
  days?: string[]
  /** Hours between runs, 1 to 23. */
  every?: number
  cron?: string
  /** An IANA name; empty is UTC. */
  timeZone?: string
}

export interface S3Settings {
  enabled: boolean
  bucket: string
  prefix?: string
  region?: string
  endpoint?: string
  pathStyle?: boolean
  caPem?: string
  /** keys: an access key kept in the Secret; identity: the pod's own (IRSA, EKS Pod Identity). */
  credentials: 'keys' | 'identity'
  schedule: ScheduleSpec
  retention: { mode: 'count' | 'days' | 'lifecycle'; count?: number; days?: number }
  /** '' (the bucket's default), AES256 (SSE-S3) or aws:kms (SSE-KMS). */
  sse?: '' | 'AES256' | 'aws:kms'
  kmsKeyId?: string
  /** Seal each file on the console with the Secret's passphrase. */
  encrypt?: boolean
  /** Take the optional parts too (chat conversations). */
  optional?: boolean
  since?: string
  by?: string
}

/** One backup to S3, or a scheduled time let go. */
export interface S3Run {
  started: string
  ended?: string
  slot?: string
  by: string
  key?: string
  size?: number
  ok: boolean
  missed?: boolean
  error?: string
  pruned?: number
}

export interface S3View {
  settings: S3Settings
  saved: boolean
  /** Whether the Secret holds an access key and a passphrase: never the values. */
  keys: { accessKey: boolean; passphrase: boolean }
  /** Whether the pod has an AWS identity of its own. */
  identity: boolean
  schedule: string
  next?: string
  running: boolean
  status: { handled?: string; recent?: S3Run[]; lastOk?: S3Run }
}

/** Keys typed with Replace…: each absent is left as it is. */
export interface S3Keys {
  accessKeyId?: string
  secretAccessKey?: string
  passphrase?: string
}

export interface S3Step {
  name: string
  ok: boolean
  detail?: string
}

export interface S3Backup {
  key: string
  size: number
  modified: string
  taken: string
  encrypted: boolean
}

export const fetchBackupS3 = () => request<S3View>('GET', '/api/v1/admin/backup/s3')
export const saveBackupS3 = (settings: S3Settings, keys: S3Keys) => request<S3View>('PUT', '/api/v1/admin/backup/s3', { settings, keys })
export const testBackupS3 = (settings: S3Settings, keys: S3Keys) => request<{ steps: S3Step[] }>('POST', '/api/v1/admin/backup/s3/test', { settings, keys }).then((r) => r.steps ?? [])
export const runBackupS3 = () => request<S3Run>('POST', '/api/v1/admin/backup/s3/run')
export const describeSchedule = (s: ScheduleSpec) => request<{ description?: string; next?: string[]; error?: string }>('POST', '/api/v1/admin/backup/s3/schedule', s)
export const listBackupsS3 = () => request<{ backups: S3Backup[] }>('GET', '/api/v1/admin/backup/s3/objects').then((r) => r.backups ?? [])
export const fetchBackupFromS3 = (key: string) => request<BackupFile>('GET', '/api/v1/admin/backup/s3/object?key=' + enc(key))
