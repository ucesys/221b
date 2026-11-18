import { useEffect, useRef, useState } from 'react'
import type { ClusterInfo, Me, Settings } from '../api/types'
import { ADMIN_PAGES, adminHref, AUTH_PAGES, authHref, currentRoute, HOME, navigate, withParams, type AdminPage as Page, type AuthPage, type Route } from '../lib/router'
import { PermissionsPage } from './PermissionsPage'
import { RolesPage } from './RolesPage'
import { SettingsPanel } from './SettingsPage'
import { UsersPage } from './UsersPage'
import { AdminAudit } from './AdminAudit'
import { AdminBackup } from './AdminBackup'
import { AdminSessions, AdminToolbox } from './toolbox/AdminToolbox'
import { UploadedDocuments } from './ai/UploadedDocuments'
import { expiringSoon, fetchCredentials } from '../api/credentials'
import { authPagesFor, isSuperAdmin, pagesFor } from '../lib/adminAccess'

export interface AdminProps {
  route: Route
  me: Me | null
  now: number
  cluster: ClusterInfo | null
  onToast: (msg: string) => void
}

// The tabs (CU-18, 0.12.1): Auth, AI (the agents' pages and AIOps'; not a
// tab named AIOps, which the main app's area is), Credentials, Settings,
// Logs, Toolbox and Backup.
const PAGES: { id: Page; label: string; tip: string }[] = [
  { id: 'auth', label: 'Auth', tip: 'Roles, Permissions and, for SuperAdmins, Users: who may do what' },
  { id: 'ai', label: 'AI', tip: 'SuperAdmin only: the agents’ providers, agents, limits and knowledge, and AIOps’ SLA, issues, cases and jobs' },
  { id: 'credentials', label: 'Credentials', tip: 'SuperAdmin only: the tokens and keys the console signs in with' },
  { id: 'settings', label: 'Settings', tip: 'SuperAdmin only: getting started, general, Watson and repositories' },
  // Approvals moved to AIOps › Inbox (Phase 12, CS-06); #/admin/approvals opens it there.
  { id: 'logs', label: 'Logs', tip: 'SuperAdmin only: the Toolbox’s sessions, and the audit log of who did what' },
  { id: 'toolbox', label: 'Toolbox', tip: 'SuperAdmin only: what the chart allows the Toolbox' },
  { id: 'backup', label: 'Backup', tip: 'SuperAdmin only: a backup of the console’s own data, and a restore' },
]

const AUTH: { id: AuthPage; label: string; tip: string }[] = [
  { id: 'roles', label: 'Roles', tip: 'The four roles and who holds them' },
  { id: 'permissions', label: 'Permissions', tip: 'What each role may read and change' },
  { id: 'users', label: 'Users', tip: 'SuperAdmin only: the people 221B knows' },
]

// Who is offered which page lives in lib/adminAccess, which Sherlock's
// view_admin asks too (SR-13).
export { authPagesFor, isSuperAdmin, pagesFor }

type Log = 'sessions' | 'audit'

/** Administration › Logs: the Toolbox's sessions, and the audit log (#/admin/logs/audit). */
function Logs(props: AdminProps) {
  const { route } = props
  const log: Log = route.path[1] === 'audit' ? 'audit' : 'sessions'
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="admin-logs">
      <div className="rk-seg" role="group" aria-label="Log" style={{ alignSelf: 'flex-start' }}>
        {(
          [
            ['sessions', 'Sessions'],
            ['audit', 'Audit log'],
          ] as const
        ).map(([id, label]) => (
          <button key={id} type="button" aria-pressed={log === id} onClick={() => navigate(adminHref('logs', [id], route.cluster), true)} data-testid={`logs-${id}`}>
            {label}
          </button>
        ))}
      </div>
      {log === 'sessions' ? <AdminSessions {...props} /> : <AdminAudit {...props} />}
    </div>
  )
}

/** The link of one of Auth's pages: Roles and Permissions describe the cluster open, Users the console. */
const authLink = (page: AuthPage, cluster: string) => authHref(page, [], page === 'users' ? 'local' : cluster)

/**
 * Administration › Auth (CU-18): Roles, Permissions and Users, which were
 * tabs of their own. A page the person's role does not open is refused
 * as before; the pages read their item at route.path[2].
 */
function Auth(props: AdminProps & { sub: AuthPage; offered: AuthPage[] }) {
  const { route, sub, offered } = props
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="admin-auth">
      {offered.length > 1 && (
        <div className="rk-seg" role="group" aria-label="Auth" style={{ alignSelf: 'flex-start' }}>
          {AUTH.filter((p) => offered.includes(p.id)).map((p) => (
            <button key={p.id} type="button" aria-pressed={sub === p.id} title={p.tip} onClick={() => navigate(authLink(p.id, route.cluster))} data-testid={`auth-${p.id}`}>
              {p.label}
            </button>
          ))}
        </div>
      )}
      {!offered.includes(sub) && <Refusal label={AUTH.find((p) => p.id === sub)!.label} signedIn />}
      {offered.includes(sub) && sub === 'roles' && <RolesPage {...props} />}
      {offered.includes(sub) && sub === 'permissions' && <PermissionsPage {...props} />}
      {offered.includes(sub) && sub === 'users' && <UsersPage {...props} />}
    </div>
  )
}

// Where Administration was opened from, to go back to.
let back = HOME

/** Opens a tab of the Administration area, remembering the page to return to. */
export function openAdmin(page: Page = 'auth'): void {
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

/**
 * SEC-30: requests came through a proxy the console does not trust, so it
 * sees the proxy's address for everyone behind it.
 */
export function UntrustedProxyNotice({ me }: { me: Me | null }) {
  if (!me?.admin || !me.untrustedProxy) return null
  return (
    <p className="rk-callout" data-tone="warn" role="status" style={{ margin: 0 }} data-testid="untrusted-proxy">
      Requests reach the console through a proxy it does not trust (from {me.untrustedProxy}): the sign-in limit per address and the audit log see the proxy’s address. List the proxy under <code>auth.trustedProxies</code>.
    </p>
  )
}

function Refusal({ label, signedIn }: { label: string; signedIn: boolean }) {
  return (
    <section className="rk-card" aria-label={`${label}, not available`} style={{ padding: '20px 24px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="admin-refusal">
      <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{label} is for SuperAdmins</h2>
      <p style={{ margin: 0, color: 'var(--fg2)' }}>{signedIn ? `Your role does not include this page. Roles and Permissions show what each role may do; ask a SuperAdmin if you need a change.` : 'Sign in to see this page.'}</p>
    </section>
  )
}

/**
 * Administration › AI › Knowledge for someone who is not a SuperAdmin:
 * the Uploaded documents section alone, for an Admin (MY-11, 0.12.1): the
 * notes and documents they add and remove; anyone else is refused, as for
 * the rest of AI.
 */
function AdminKnowledge({ onToast, focus }: { onToast: (msg: string) => void; focus: boolean }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="admin-knowledge-added">
      <UploadedDocuments
        onToast={onToast}
        focus={focus}
        fallback={<Refusal label="AI" signedIn />}
        after={
          <p className="rk-fhint" style={{ margin: 0 }}>
            The rest of Administration › AI › Knowledge, Mycroft’s sources and where the wiki is kept, is for SuperAdmins.
          </p>
        }
      />
    </div>
  )
}

/** How many credentials expire within 14 days, for the Credentials tab (CR-08); 0 for whoever does not see it. */
function useCredentialsSoon(on: boolean, page: Page): number {
  const [soon, setSoon] = useState(0)
  useEffect(() => {
    if (!on) return
    let stop = false
    fetchCredentials().then(
      (v) => !stop && setSoon(expiringSoon(v.credentials ?? []).length),
      () => undefined,
    )
    return () => {
      stop = true
    }
  }, [on, page])
  return on ? soon : 0
}

/**
 * The Administration area (RB-41; CU-18): Auth, AI, Credentials, Settings,
 * Logs, Toolbox and Backup under one heading. Back or Escape returns to
 * where it was opened from. An address of before shows the page it leads
 * to, and the address bar is put right (CU-19).
 */
export function AdminPageView({ route, me, now, cluster, onToast, settings, refreshedAt }: Props) {
  const offered = pagesFor(me)
  const asked = route.path[0] as Page | undefined
  const page: Page = asked && ADMIN_PAGES.includes(asked) ? asked : (offered[0] ?? 'auth')
  const refused = !!me && !offered.includes(page)
  const authOffered = authPagesFor(me)
  const askedAuth = route.path[1] as AuthPage | undefined
  const sub: AuthPage = page === 'auth' && askedAuth && AUTH_PAGES.includes(askedAuth) ? askedAuth : (authOffered[0] ?? 'permissions')
  const heading = useRef<HTMLHeadingElement>(null)
  const credsSoon = useCredentialsSoon(isSuperAdmin(me), page)

  // An address of before (#/admin/roles, #/admin/settings/agents/…) is
  // put in the bar as it is now.
  const canonical = withParams(route, {})
  useEffect(() => {
    if (location.hash !== canonical) navigate(canonical, true)
  }, [canonical])

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
      // Escape in the Sherlock and Watson panel is the panel's.
      if (t?.closest('[data-testid="sherlock-dock"]')) return
      e.preventDefault()
      closeAdmin()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const props: AdminProps = { route, me, now, cluster, onToast }
  const label = PAGES.find((p) => p.id === page)!.label
  // AI › Knowledge's Uploaded documents section is where Admins add to the wiki too (MY-04, MY-11): the rest of AI stays closed to them.
  const knowledge = page === 'ai' && route.path[1] === 'knowledge'
  return (
    <main
      className="rk-main"
      style={{ flex: '1 1 auto', display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '24px 20px 48px', overflow: 'auto', background: 'var(--bg)' }}
      aria-label="Administration"
      data-testid="admin-page"
    >
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
              <a
                key={p.id}
                className="rk-tab"
                href={p.id === 'auth' ? authLink(authOffered[0] ?? 'permissions', route.cluster) : adminHref(p.id, [], p.id === 'logs' || p.id === 'toolbox' ? route.cluster : 'local')}
                aria-current={p.id === page ? 'page' : undefined}
                title={p.id === 'credentials' && credsSoon > 0 ? `${credsSoon} ${credsSoon === 1 ? 'expires' : 'expire'} within 14 days` : p.tip}
                data-admin-page={p.id}
                style={{ height: 36 }}
              >
                {p.label}
                {p.id === 'credentials' && credsSoon > 0 && (
                  <span className="rk-tag" data-tone="warn" style={{ marginLeft: 6 }} data-testid="admin-credentials-soon">
                    {credsSoon}
                  </span>
                )}
              </a>
            ))}
          </nav>
        </div>
        <h2 ref={heading} tabIndex={-1} className="rk-sr" style={{ outline: 'none' }}>
          {label}
        </h2>
        {me === null && <p style={{ margin: 0, color: 'var(--fg3)' }}>Loading…</p>}
        <UntrustedProxyNotice me={me} />
        {me && refused && knowledge && <AdminKnowledge onToast={onToast} focus={route.path[2] === 'added' || route.path[2] === 'uploads'} />}
        {me && refused && !knowledge && <Refusal label={label} signedIn />}
        {me && !refused && page === 'auth' && <Auth {...props} sub={sub} offered={authOffered} />}
        {me && !refused && page === 'toolbox' && <AdminToolbox {...props} />}
        {me && !refused && page === 'logs' && <Logs {...props} />}
        {me && !refused && (page === 'ai' || page === 'credentials' || page === 'settings') && <SettingsPanel route={route} settings={settings} refreshedAt={refreshedAt} onToast={onToast} />}
        {me && !refused && page === 'backup' && <AdminBackup {...props} />}
      </div>
    </main>
  )
}
