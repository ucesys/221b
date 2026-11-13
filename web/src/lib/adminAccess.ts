// Who is offered which page of the Administration area (RB-41), shared by
// the area itself and by Sherlock's view_admin (SR-13), so a page the
// person's role does not open is refused the same way in both.

import type { Me } from '../api/types'
import { ADMIN_PAGES, type AdminPage } from './router'

/** The pages for SuperAdmins alone. */
export const SUPER_ONLY: ReadonlySet<AdminPage> = new Set<AdminPage>(['users', 'toolbox', 'logs', 'settings', 'backup'])

/** Whether this person is a SuperAdmin; with sign-in off nobody is. */
export function isSuperAdmin(me: Me | null): boolean {
  return !!me && me.admin
}

/**
 * The pages this person is offered (RB-41): Roles and Permissions for
 * everyone, Users and Settings for a SuperAdmin. With sign-in off there are
 * no users and no roles to list, and nobody is a SuperAdmin, so it offers
 * Permissions alone.
 */
export function pagesFor(me: Me | null): AdminPage[] {
  if (!me) return []
  if (me.mode === 'none') return ['permissions']
  return ADMIN_PAGES.filter((p) => isSuperAdmin(me) || !SUPER_ONLY.has(p))
}

/** Whether this person sees Settings › Knowledge's Added in Mycroft card: an Admin does (MY-11). */
export function seesAddedInMycroft(me: Me | null): boolean {
  return isSuperAdmin(me) || !!me?.roles?.includes('admin')
}
