// The Administration area's data (RB-41 to RB-47): the four roles and who
// holds them, what each may do, the users 221B knows, and the changes a
// SuperAdmin makes to local users. Nothing here changes a cluster: roles
// and bindings come from the chart (RB-52), so there is no call that
// writes one. Hashes and credentials never come back (X-04).

import { useCallback, useEffect, useRef, useState } from 'react'
import { apiPath } from '../lib/router'
import { unauthorized } from './auth'

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
  /** A reset: the new password; it ends the person's sessions (RB-46). */
  password?: string
}

/** The server's refusal, with its message and status. */
export class AdminError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(url, {
      method,
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new AdminError('The server could not be reached.', 0)
  }
  if (!res.ok) {
    if (unauthorized(res)) throw new AdminError('Your session has ended; sign in again.', 401)
    let msg = `The server answered ${res.status}.`
    try {
      const b = (await res.json()) as { error?: string }
      if (b.error) msg = b.error
    } catch {
      // Not JSON: keep the status.
    }
    throw new AdminError(msg, res.status)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const enc = encodeURIComponent

export const fetchRoles = (cluster: string) => request<RolesResponse>('GET', apiPath('admin/roles', cluster))
export const fetchPermissions = (cluster: string) => request<PermissionsResponse>('GET', apiPath('admin/permissions', cluster))
export const fetchUsers = () => request<UsersResponse>('GET', '/api/v1/admin/users')

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

/** Loads one admin resource, again when its key changes or reload is called, and again every `every` ms while the page is open. */
export function useAdmin<T>(key: string, load: () => Promise<T>, every = 0): Loaded<T> {
  const [state, setState] = useState<{ key: string; data: T | null; error: string; status: number }>({ key, data: null, error: '', status: 0 })
  const [tick, setTick] = useState(0)
  const loader = useRef(load)
  useEffect(() => {
    loader.current = load
  })
  useEffect(() => {
    let gone = false
    const run = () => {
      loader.current().then(
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
    const t = every > 0 ? setInterval(run, every) : undefined
    return () => {
      gone = true
      if (t) clearInterval(t)
    }
  }, [key, tick, every])
  const reload = useCallback(() => setTick((t) => t + 1), [])
  const current = state.key === key
  return { data: current ? state.data : null, error: current ? state.error : '', status: current ? state.status : 0, loading: !current || (state.data === null && state.error === ''), reload }
}
