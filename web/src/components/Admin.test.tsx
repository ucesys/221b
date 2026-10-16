// The Administration area (RB-41 to RB-47): who sees which page, the Roles,
// Permissions and Users pages from a server's answers, and the changes a
// SuperAdmin makes, each behind a confirmation (RB-51).
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PermissionsResponse, RolesResponse, UsersResponse } from '../api/admin'
import type { Me } from '../api/types'
import { parseHash } from '../lib/router'
import { AdminPageView, pagesFor } from './AdminPage'
import { PermissionsPage } from './PermissionsPage'
import { RolesPage } from './RolesPage'
import { UsersPage } from './UsersPage'

const me = (over: Partial<Me> = {}): Me => ({ mode: 'local', user: { subject: 'mikhail', username: 'mikhail', name: 'Mikhail' }, admin: true, roles: ['superAdmin'], method: 'local', sessionIdleSeconds: 3600, ...over })
const viewer = me({ admin: false, roles: ['viewer'], user: { subject: 'bob', username: 'bob' } })

const roles: RolesResponse = {
  visible: true,
  definitions: { admin: 'full', superAdmin: 'readOnly' },
  nodesRole: '221b-nodes',
  roles: [
    { id: 'superAdmin', name: 'SuperAdmin', description: 'Everything', scope: 'Every namespace, every cluster', definition: 'readOnly', clusterRole: '221b-super-admin', present: true, can: ['Read everything'], cannot: ['Change anything'], subjects: [{ kind: 'Group', name: 'platform-admins', source: 'ldap', namespaces: [], managed: true }], holders: { groups: 1, users: 2 } },
    { id: 'superViewer', name: 'SuperViewer', description: 'Reads', scope: 'Every namespace, every cluster', definition: 'readOnly', clusterRole: '221b-super-viewer', present: true, can: [], cannot: [], subjects: [], holders: { groups: 0, users: 0 } },
    {
      id: 'admin',
      name: 'Admin',
      description: 'Viewer plus changes',
      scope: 'Bound namespaces',
      definition: 'full',
      clusterRole: '221b-admin',
      present: true,
      can: ['Scale', 'Restart'],
      cannot: ['Change roles'],
      holders: { groups: 2, users: 0 },
      subjects: [
        { kind: 'Group', name: 'checkout-ops', source: 'local', namespaces: ['checkout', 'web'], members: 2, managed: true },
        { kind: 'User', name: 'carol', source: 'other', namespaces: ['web'], managed: false, orphaned: true },
      ],
    },
    { id: 'viewer', name: 'Viewer', description: 'Reads', scope: 'Bound namespaces', definition: 'readOnly', clusterRole: '221b-viewer', present: true, can: [], cannot: [], subjects: [], holders: { groups: 0, users: 0 } },
  ],
}

const perms: PermissionsResponse = {
  definitions: { admin: 'full', superAdmin: 'readOnly' },
  enforce: 'off',
  checkedAt: '2026-10-16T12:04:00Z',
  drift: { state: 'differs', roles: [{ id: 'admin', differences: ['adds delete on nodes'] }] },
  roles: [
    { id: 'superAdmin', name: 'SuperAdmin', clusterRole: '221b-super-admin', definition: 'readOnly', present: true, rules: [{ group: '', resource: 'secrets', verbs: ['get', 'list', 'watch'], kind: 'read', scope: 'all', flags: ['Secrets'] }] },
    { id: 'superViewer', name: 'SuperViewer', clusterRole: '221b-super-viewer', definition: 'readOnly', present: true, rules: [] },
    { id: 'admin', name: 'Admin', clusterRole: '221b-admin', definition: 'full', present: true, rules: [{ group: 'apps', resource: 'deployments', verbs: ['get', 'list', 'watch', 'patch'], kind: 'write', scope: 'bound', flags: [] }] },
    { id: 'viewer', name: 'Viewer', clusterRole: '221b-viewer', definition: 'readOnly', present: true, rules: [{ group: 'apps', resource: 'deployments', verbs: ['get', 'list', 'watch'], kind: 'read', scope: 'bound', flags: [] }] },
  ],
}

const users: UsersResponse = {
  canCreate: true,
  persistent: true,
  groups: [
    { name: 'web-readers', role: 'viewer', namespaces: ['web'], members: 1 },
    { name: 'checkout-ops', role: 'admin', namespaces: ['checkout', 'web'], members: 1 },
  ],
  ldap: { url: 'ldaps://ldap.corp.example:636', state: 'connected', people: 214, groups: 6, errors: [], running: false, lastSync: '2026-10-16T12:04:00Z' },
  users: [
    { id: 'local:bob', username: 'bob', name: 'bob', email: 'bob@corp.example', source: 'local', status: 'active', holdings: [{ role: 'admin', via: 'checkout-ops', namespaces: ['checkout', 'web'] }], groups: ['checkout-ops'], superRoles: [], managed: false, kubeName: '221b-local:bob' },
    { id: 'ldap:priya', username: 'priya', name: 'Priya Raman', source: 'ldap', status: 'active', holdings: [{ role: 'superAdmin', via: 'platform-admins' }], groups: ['platform-admins'], superRoles: [], managed: false, kubeName: '221b-ldap:priya' },
    { id: 'local:break-glass', username: 'break-glass', source: 'local', status: 'active', holdings: [{ role: 'superAdmin' }], groups: [], superRoles: ['superAdmin'], managed: true, kubeName: '221b-local:break-glass' },
  ],
}

type Handler = (method: string, url: string, body: unknown) => { status?: number; body?: unknown }
let calls: { method: string; url: string; body: unknown }[] = []

function serve(h: Handler) {
  calls = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      const method = init?.method ?? 'GET'
      calls.push({ method, url, body })
      const r = h(method, url, body)
      const status = r.status ?? 200
      return { ok: status < 400, status, json: async () => r.body ?? {} } as Response
    }),
  )
}

const common = (hash: string, who: Me = me()) => ({ route: parseHash(hash), me: who, now: Date.parse('2026-10-16T13:00:00Z'), cluster: null, onToast: vi.fn() })

beforeEach(() => {
  // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
  Element.prototype.scrollTo = () => {}
  serve((_method, url) => {
    if (url.endsWith('/admin/roles')) return { body: roles }
    if (url.endsWith('/admin/permissions')) return { body: perms }
    if (url.endsWith('/admin/users')) return { body: users }
    return {}
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('who is offered which page (RB-41, RB-31)', () => {
  it('offers Roles and Permissions to everyone, Users and Settings to a SuperAdmin', () => {
    expect(pagesFor(me())).toEqual(['roles', 'permissions', 'users', 'settings'])
    expect(pagesFor(viewer)).toEqual(['roles', 'permissions'])
    expect(pagesFor(null)).toEqual([])
  })

  it('offers Permissions and Settings with sign-in off, where there are no users or roles to manage', () => {
    expect(pagesFor(me({ mode: 'none' }))).toEqual(['permissions', 'settings'])
  })

  it('refuses a Viewer who opens Users, and says why', () => {
    render(<AdminPageView {...common('#/admin/users', viewer)} settings={null} refreshedAt={null} />)
    expect(screen.getByTestId('admin-refusal').textContent).toContain('Users is for SuperAdmins')
    expect(screen.queryByRole('link', { name: /^Users/ })).toBeNull()
    expect(calls).toHaveLength(0)
  })
})

describe('Roles (RB-42)', () => {
  it('lists the four roles with the definitions in use', async () => {
    const { container } = render(<RolesPage {...common('#/admin/roles')} />)
    await waitFor(() => expect(container.querySelectorAll('[data-role]')).toHaveLength(4))
    expect(screen.getByTestId('role-definitions').textContent).toContain('SuperAdmin: read-only')
    expect(container.querySelector('[data-role="admin"]')!.textContent).toContain('221b-admin')
    // Nothing on the page edits a role (RB-01).
    expect(screen.queryByText(/edit role|delete role|new role/i)).toBeNull()
  })

  it('opens a role: its holders, a local group with Add member for a SuperAdmin, and a binding by hand with no remove', async () => {
    render(<RolesPage {...common('#/admin/roles/admin')} />)
    const drawer = await screen.findByTestId('role-drawer')
    const subjects = within(drawer).getByTestId('role-subjects')
    expect(within(subjects).getByText('checkout-ops')).toBeTruthy()
    expect(subjects.textContent).toContain('Local group · 2 members')
    expect(within(subjects).getByRole('button', { name: 'Add a member to checkout-ops' })).toBeTruthy()
    const carol = subjects.querySelector('[data-subject="carol"]')!
    expect(carol.textContent).toContain('Not managed by 221B')
    expect(carol.textContent).toContain('Orphaned: no such user')
    expect(within(carol as HTMLElement).queryByRole('button')).toBeNull()
    expect(within(carol as HTMLElement).getByRole('link', { name: 'Open in Access' }).getAttribute('href')).toBe('#/access?q=carol')
  })

  it('gives a Viewer no Add member', async () => {
    render(<RolesPage {...common('#/admin/roles/admin', viewer)} />)
    const drawer = await screen.findByTestId('role-drawer')
    expect(within(drawer).queryByRole('button', { name: /Add a member/ })).toBeNull()
  })

  it('shows nothing to a person who cannot list RoleBindings (RB-41)', async () => {
    serve(() => ({ body: { ...roles, visible: false, roles: [] } }))
    render(<RolesPage {...common('#/admin/roles', viewer)} />)
    expect((await screen.findByTestId('roles-hidden')).textContent).toContain('cannot list RoleBindings')
  })
})

describe('Permissions (RB-44)', () => {
  it('says the live roles differ and names the rule, with totals for each role', async () => {
    const { container } = render(<PermissionsPage {...common('#/admin/permissions')} />)
    await waitFor(() => expect(container.querySelectorAll('[data-perm]').length).toBeGreaterThan(0))
    expect(screen.getByTestId('drift').textContent).toContain('Differs from the shipped definitions: Admin.')
    expect(screen.getByTestId('drift-details').textContent).toContain('Admin: adds delete on nodes')
    expect(container.querySelector('[data-role-summary="admin"]')!.textContent).toContain('reads 1 kind, changes 1')
    expect(screen.getByText('Strict mode: off')).toBeTruthy()
    expect(screen.getByText('SuperAdmin: read-only')).toBeTruthy()
  })

  it('splits Read from Write and finds the sensitive rows', async () => {
    const { container } = render(<PermissionsPage {...common('#/admin/permissions')} />)
    await waitFor(() => expect(container.querySelectorAll('[data-perm]').length).toBe(3))
    fireEvent.click(screen.getByRole('button', { name: 'Write' }))
    expect([...container.querySelectorAll('[data-perm]')].map((r) => r.getAttribute('data-perm'))).toEqual(['write:apps/deployments'])
    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    fireEvent.click(screen.getByLabelText('Sensitive only'))
    expect([...container.querySelectorAll('[data-perm]')].map((r) => r.getAttribute('data-perm'))).toEqual(['read:/secrets'])
  })
})

describe('Users (RB-45, RB-46)', () => {
  it('lists people with their source, shows the LDAP strip and the toolbar with Create local user', async () => {
    const { container } = render(<UsersPage {...common('#/admin/users')} />)
    await waitFor(() => expect(container.querySelectorAll('[data-user]')).toHaveLength(3))
    expect(screen.getByTestId('ldap-strip').textContent).toContain('214 people in 6 listed groups')
    expect(screen.getByTestId('create-user-btn')).toBeTruthy()
    expect(container.querySelector('[data-user="ldap:priya"]')!.textContent).toContain('LDAP')
    expect(container.querySelector('[data-user="local:break-glass"]')!.textContent).toContain('Helm')
    expect(screen.getByRole('button', { name: /Export/ })).toBeTruthy()
  })

  it('does not offer to create users where the directory owns them (RB-28)', async () => {
    serve(() => ({ body: { ...users, canCreate: false } }))
    const { container } = render(<UsersPage {...common('#/admin/users')} />)
    await waitFor(() => expect(container.querySelectorAll('[data-user]')).toHaveLength(3))
    expect(screen.queryByTestId('create-user-btn')).toBeNull()
  })

  it('starts one sync when Refresh is clicked, and shows it running', async () => {
    serve((method, url) => {
      if (method === 'POST' && url.endsWith('/admin/ldap/refresh')) return { body: { ...users.ldap, running: true } }
      return { body: users }
    })
    render(<UsersPage {...common('#/admin/users')} />)
    fireEvent.click(await screen.findByTestId('ldap-refresh'))
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1))
  })

  it('creates a local user, refusing a reserved name and a short password before it asks the server', async () => {
    serve((method, url, body) => {
      if (method === 'POST' && url.endsWith('/admin/users')) return { body: { ...users.users[0], username: (body as { username: string }).username } }
      return { body: users }
    })
    render(<UsersPage {...common('#/admin/users')} />)
    fireEvent.click(await screen.findByTestId('create-user-btn'))
    const form = screen.getByTestId('create-user')
    fireEvent.change(within(form).getByTestId('new-username'), { target: { value: 'system:masters' } })
    fireEvent.change(within(form).getByTestId('new-password'), { target: { value: 'short' } })
    fireEvent.click(within(form).getByTestId('create-submit'))
    expect(within(form).getByText('This name is reserved.')).toBeTruthy()
    expect(within(form).getByText('Use at least 12 characters.')).toBeTruthy()
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)
    fireEvent.change(within(form).getByTestId('new-username'), { target: { value: 'dana' } })
    fireEvent.change(within(form).getByTestId('new-password'), { target: { value: 'a-long-enough-password' } })
    fireEvent.click(within(form).getByTestId('create-submit'))
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'POST')!.body).toMatchObject({ username: 'dana', password: 'a-long-enough-password', groups: [] })
  })

  it('removes a local user from a group only after a confirmation that says what happens (RB-51)', async () => {
    serve((method, url) => {
      if (method === 'POST' && url.includes('/groups')) return { body: users.users[0] }
      return { body: users }
    })
    render(<UsersPage {...common('#/admin/users/bob')} />)
    const drawer = await screen.findByTestId('user-drawer')
    expect(drawer.textContent).toContain('221b-local:bob')
    fireEvent.click(within(drawer).getByRole('button', { name: 'Remove bob from checkout-ops' }))
    const dialog = screen.getByTestId('confirm')
    expect(dialog.textContent).toContain('within 2 minutes')
    expect(dialog.textContent).toContain('Nothing is written to Kubernetes')
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)
    fireEvent.click(within(dialog).getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ url: '/api/v1/admin/users/bob/groups', body: { remove: ['checkout-ops'] } })
  })

  it('gives LDAP people and chart users no account actions, only what the directory or values say', async () => {
    render(<UsersPage {...common('#/admin/users/priya')} />)
    const drawer = await screen.findByTestId('user-drawer')
    expect(within(drawer).queryByRole('button', { name: 'Reset password' })).toBeNull()
    expect(within(drawer).queryByRole('button', { name: 'Delete' })).toBeNull()
    expect(drawer.textContent).toContain('The directory owns this person')
    cleanup()
    render(<UsersPage {...common('#/admin/users/break-glass')} />)
    const d2 = await screen.findByTestId('user-drawer')
    expect(within(d2).queryByRole('button', { name: 'Delete' })).toBeNull()
    expect(d2.textContent).toContain('Defined in values.yaml')
  })

  it('shows the add-to-group form with what the person can do afterwards, and the groups they are not in', async () => {
    render(<UsersPage {...common('#/admin/users/bob')} />)
    fireEvent.click(await screen.findByTestId('user-add-group'))
    const form = screen.getByTestId('add-to-group')
    const select = within(form).getByTestId('add-group') as HTMLSelectElement
    expect(select.value).toBe('web-readers')
    expect(within(form).getByTestId('after-joining').textContent).toContain('Viewer')
    expect(within(form).getByTestId('after-joining').textContent).toContain('through web-readers')
    expect(within(form).getByTestId('after-joining').textContent).toContain('through checkout-ops')
  })
})
