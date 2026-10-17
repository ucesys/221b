import { render, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ownRole, ROLE_COLUMNS, type RoleView } from './access'

describe('221B’s own roles in the Access tab (RB-49)', () => {
  it('knows the four roles and the nodes role by name, as ClusterRoles only', () => {
    expect(ownRole({ name: '221b-admin', kind: 'ClusterRole' })).toBe('admin')
    expect(ownRole({ name: '221b-super-viewer', kind: 'ClusterRole' })).toBe('superViewer')
    expect(ownRole({ name: '221b-nodes', kind: 'ClusterRole' })).toBe('nodes')
    expect(ownRole({ name: '221b-admin', kind: 'Role' })).toBeNull()
    expect(ownRole({ name: 'admin', kind: 'ClusterRole' })).toBeNull()
    expect(ownRole({ name: '221b-console', kind: 'ClusterRole' })).toBeNull()
  })

  it('marks one with a link that opens it in Administration', () => {
    const role = { name: '221b-admin', kind: 'ClusterRole' } as RoleView
    const { getByTestId } = render(<>{ROLE_COLUMNS[0]!.cell(role, 0)}</>)
    const tag = getByTestId('own-role')
    expect(tag.textContent).toBe('221B role')
    expect(tag.getAttribute('href')).toBe('#/admin/roles/admin')
    const plain = render(<>{ROLE_COLUMNS[0]!.cell({ name: 'edit', kind: 'ClusterRole' } as RoleView, 0)}</>)
    expect(within(plain.container).queryByTestId('own-role')).toBeNull()
  })
})
