// Who is offered which page of the Administration area (RB-41), shared by
// the area itself and by Sherlock's view_admin (SR-13), so a page the
// person's role does not open is refused the same way in both.

import type { Me } from '../api/types'
import { ADMIN_PAGES, AUTH_PAGES, type AdminPage, type AuthPage } from './router'

/** The tabs for SuperAdmins alone: all but Auth (CU-18). */
export const SUPER_ONLY: ReadonlySet<AdminPage> = new Set<AdminPage>(['ai', 'credentials', 'settings', 'logs', 'toolbox', 'backup'])

/** Whether this person is a SuperAdmin; with sign-in off nobody is. */
export function isSuperAdmin(me: Me | null): boolean {
  return !!me && me.admin
}

/**
 * The tabs this person is offered (RB-41, CU-18): Auth for everyone, the
 * rest for a SuperAdmin. With sign-in off nobody is a SuperAdmin, so it
 * offers Auth alone, with Permissions alone in it (authPagesFor).
 */
export function pagesFor(me: Me | null): AdminPage[] {
  if (!me) return []
  if (me.mode === 'none') return ['auth']
  return ADMIN_PAGES.filter((p) => isSuperAdmin(me) || !SUPER_ONLY.has(p))
}

/**
 * Auth's pages this person is offered: Roles and Permissions for everyone,
 * Users for a SuperAdmin. With sign-in off there are no users and no roles
 * to list, so it offers Permissions alone.
 */
export function authPagesFor(me: Me | null): AuthPage[] {
  if (!me) return []
  if (me.mode === 'none') return ['permissions']
  return AUTH_PAGES.filter((p) => p !== 'users' || isSuperAdmin(me))
}

/** Whether this person sees Administration › AI › Knowledge's Uploaded documents section: an Admin does (MY-11, 0.12.1). */
export function seesAddedInMycroft(me: Me | null): boolean {
  return isSuperAdmin(me) || !!me?.roles?.includes('admin')
}
