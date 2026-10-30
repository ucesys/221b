import { useEffect, useRef } from 'react'
import type { ClusterInfo, Me, Settings } from '../api/types'
import { ADMIN_PAGES, adminHref, currentRoute, HOME, navigate, type AdminPage as Page, type Route } from '../lib/router'
import { PermissionsPage } from './PermissionsPage'
import { RolesPage } from './RolesPage'
import { SettingsPanel } from './SettingsPage'
import { UsersPage } from './UsersPage'
import { AdminAudit } from './AdminAudit'
import { AdminBackup } from './AdminBackup'
import { AdminSessions, AdminToolbox } from './toolbox/AdminToolbox'

export interface AdminProps {
  route: Route
  me: Me | null
  now: number
  cluster: ClusterInfo | null
  onToast: (msg: string) => void
}

const PAGES: { id: Page; label: string; tip: string; superOnly: boolean }[] = [
  { id: 'roles', label: 'Roles', tip: 'The four roles and who holds them', superOnly: false },
  { id: 'permissions', label: 'Permissions', tip: 'What each role may read and change', superOnly: false },
  { id: 'users', label: 'Users', tip: 'SuperAdmin only: the people 221B knows', superOnly: true },
  { id: 'sessions', label: 'Sessions', tip: 'SuperAdmin only: the Toolbox’s sessions, open and ended', superOnly: true },
  { id: 'toolbox', label: 'Toolbox', tip: 'SuperAdmin only: what the chart allows the Toolbox', superOnly: true },
  { id: 'audit', label: 'Audit log', tip: 'SuperAdmin only: who did what, and when', superOnly: true },
  { id: 'settings', label: 'Settings', tip: 'SuperAdmin only: refresh rate, Sherlock and Watson', superOnly: true },
  { id: 'backup', label: 'Backup', tip: 'SuperAdmin only: a backup of the console’s own data, and a restore', superOnly: true },
]

/** Whether this person is a SuperAdmin; with sign-in off everyone is (RB-31). */
export function isSuperAdmin(me: Me | null): boolean {
  return !!me && me.admin
}

/**
 * The pages this person is offered (RB-41): Roles and Permissions for
 * everyone, Users and Settings for a SuperAdmin. With sign-in off there are
 * no users and no roles to list, so it offers Permissions and Settings.
 */
export function pagesFor(me: Me | null): Page[] {
  if (!me) return []
  if (me.mode === 'none') return ['permissions', 'sessions', 'toolbox', 'audit', 'settings', 'backup']
  return ADMIN_PAGES.filter((p) => isSuperAdmin(me) || !PAGES.find((x) => x.id === p)!.superOnly)
}

// Where Administration was opened from, to go back to.
let back = HOME

/** Opens a page of the Administration area, remembering the page to return to. */
export function openAdmin(page: Page = 'roles'): void {
  if (currentRoute().view !== 'admin') back = location.hash || HOME
  navigate(adminHref(page))
}

/** Returns to the page Administration was opened from, with focus on its entry. */
export function closeAdmin(): void {
  navigate(back)
  back = HOME
  requestAnimationFrame(() => document.querySelector<HTMLElement>('[data-testid="admin-link"]')?.focus())
}

interface Props extends AdminProps {
  settings: Settings | null
  refreshedAt: Date | null
}

function Refusal({ page, signedIn }: { page: Page; signedIn: boolean }) {
  const label = PAGES.find((p) => p.id === page)!.label
  return (
    <section className="rk-card" aria-label={`${label}, not available`} style={{ padding: '20px 24px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="admin-refusal">
      <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{label} is for SuperAdmins</h2>
      <p style={{ margin: 0, color: 'var(--fg2)' }}>{signedIn ? `Your role does not include this page. Roles and Permissions show what each role may do; ask a SuperAdmin if you need a change.` : 'Sign in to see this page.'}</p>
    </section>
  )
}

/**
 * The Administration area (RB-41): Roles, Permissions, Users and Settings
 * under one heading. Back or Escape returns to where it was opened from.
 */
export function AdminPageView({ route, me, now, cluster, onToast, settings, refreshedAt }: Props) {
  const offered = pagesFor(me)
  const asked = route.path[0] as Page | undefined
  const page: Page = asked && ADMIN_PAGES.includes(asked) ? asked : (offered[0] ?? 'roles')
  const refused = !!me && !offered.includes(page)
  const heading = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    heading.current?.focus({ preventScroll: true })
  }, [page])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      const t = e.target as HTMLElement | null
      // Escape in a field is the field's; elsewhere it leaves the page.
      if (t && (t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || (t.tagName === 'INPUT' && (t as HTMLInputElement).value !== ''))) return
      if (document.querySelector('dialog[open], [role="dialog"], [role="alertdialog"]')) return
      e.preventDefault()
      closeAdmin()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const props: AdminProps = { route, me, now, cluster, onToast }
  return (
    <main className="rk-main" style={{ flex: '1 1 auto', display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '24px 20px 48px', background: 'var(--bg)' }} aria-label="Administration" data-testid="admin-page">
      <div style={{ width: 'min(1280px, 100%)', display: 'flex', flexDirection: 'column', gap: 16, minWidth: 0 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 16px' }}>
          <button className="rk-btn" onClick={closeAdmin} title="Back (Esc)" data-testid="admin-back">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="m15 18-6-6 6-6" />
            </svg>
            Back
          </button>
          <h1 style={{ margin: 0, fontSize: 20, fontWeight: 600 }}>Administration</h1>
          <nav aria-label="Administration pages" style={{ display: 'flex', flexWrap: 'wrap', gap: '0 18px', marginLeft: 8 }}>
            {PAGES.filter((p) => offered.includes(p.id)).map((p) => (
              <a key={p.id} className="rk-tab" href={adminHref(p.id, [], p.id === 'roles' || p.id === 'permissions' || p.id === 'sessions' || p.id === 'toolbox' ? route.cluster : 'local')} aria-current={p.id === page ? 'page' : undefined} title={p.tip} data-admin-page={p.id} style={{ height: 36 }}>
                {p.label}
              </a>
            ))}
          </nav>
        </div>
        <h2 ref={heading} tabIndex={-1} className="rk-sr" style={{ outline: 'none' }}>
          {PAGES.find((p) => p.id === page)!.label}
        </h2>
        {me === null && <p style={{ margin: 0, color: 'var(--fg3)' }}>Loading…</p>}
        {me && refused && <Refusal page={page} signedIn />}
        {me && !refused && page === 'roles' && <RolesPage {...props} />}
        {me && !refused && page === 'permissions' && <PermissionsPage {...props} />}
        {me && !refused && page === 'users' && <UsersPage {...props} />}
        {me && !refused && page === 'sessions' && <AdminSessions {...props} />}
        {me && !refused && page === 'toolbox' && <AdminToolbox {...props} />}
        {me && !refused && page === 'audit' && <AdminAudit {...props} />}
        {me && !refused && page === 'settings' && <SettingsPanel route={route} settings={settings} refreshedAt={refreshedAt} />}
        {me && !refused && page === 'backup' && <AdminBackup {...props} />}
      </div>
    </main>
  )
}
