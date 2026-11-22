import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ClusterInfo, Me } from '../api/types'
import { parseHash } from '../lib/router'
import { TopBar, versionLine } from './TopBar'

// The account menu's last line (RB-58): the console's version and build,
// told by /api/v1/me to people signed in only, copied with a click.

const copied: string[] = []
let copyOK = true
vi.mock('../lib/clipboard', () => ({
  copyText: async (text: string) => {
    copied.push(text)
    return copyOK
  },
}))
vi.mock('./aiops/useInbox', async (original) => ({
  ...(await original<typeof import('./aiops/useInbox')>()),
  useInboxCounts: () => ({ total: 0, byKind: { case: 0, proposal: 0, approval: 0, known: 0, knowledge: 0, rule: 0 }, p0: 0, p1: 0 }),
}))
vi.mock('../lib/cases', async (original) => ({ ...(await original<typeof import('../lib/cases')>()), useIssueMe: () => null }))

const ann: Me = { mode: 'local', user: { subject: 's', username: 'ann', name: 'Ann' }, admin: true, roles: ['superAdmin'], method: 'local', sessionIdleSeconds: 0, version: '0.13.0', build: '9ef9683' }
const cluster = { id: 'local', name: 'Here', state: 'connected' } as unknown as ClusterInfo

const bar = (me: Me, onToast?: (m: string) => void) => {
  const route = parseHash('#/aiops/inbox')
  return render(
    <TopBar cluster={cluster} counts={{}} tab={null} stream="open" updatedAt={null} refreshedAt={null} settings={null} theme="light" onTheme={() => undefined} me={me} route={route} onEditCluster={() => undefined} onToast={onToast} />,
  )
}

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      disconnect() {}
      unobserve() {}
    },
  )
  copied.length = 0
  copyOK = true
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('the version in the account menu (RB-58)', () => {
  it('reads the version and the build, a release’s version alone, nothing when the server told none', () => {
    expect(versionLine(ann)).toBe('221B 0.13.0 · build 9ef9683')
    expect(versionLine({ ...ann, build: undefined })).toBe('221B 0.13.0')
    expect(versionLine({ ...ann, version: undefined, build: undefined })).toBe('')
  })

  it('is the menu’s last line, and a click copies it with a toast', async () => {
    const toasts: string[] = []
    bar(ann, (m) => toasts.push(m))
    fireEvent.click(screen.getByTestId('account'))
    const menu = screen.getByRole('menu', { name: 'Account' })
    const line = within(menu).getByTestId('account-version')
    expect(line.textContent).toBe('221B 0.13.0 · build 9ef9683')
    expect(menu.lastElementChild).toBe(line)
    fireEvent.click(line)
    await waitFor(() => expect(toasts).toEqual(['Copied 221B 0.13.0 · build 9ef9683']))
    expect(copied).toEqual(['221B 0.13.0 · build 9ef9683'])
  })

  it('says so when the browser refuses the clipboard', async () => {
    copyOK = false
    const toasts: string[] = []
    bar({ ...ann, build: undefined }, (m) => toasts.push(m))
    fireEvent.click(screen.getByTestId('account'))
    fireEvent.click(screen.getByTestId('account-version'))
    await waitFor(() => expect(toasts).toEqual(['Could not copy: the browser refused the clipboard.']))
    expect(copied).toEqual(['221B 0.13.0'])
  })

  it('is not there when the server told no version, nor with sign-in off', () => {
    bar({ ...ann, version: undefined, build: undefined })
    fireEvent.click(screen.getByTestId('account'))
    expect(screen.queryByTestId('account-version')).toBeNull()
    expect(within(screen.getByRole('menu', { name: 'Account' })).getByTestId('signout')).toBeTruthy()
    cleanup()
    bar({ ...ann, mode: 'none', version: undefined, build: undefined })
    expect(screen.queryByTestId('account')).toBeNull()
    expect(screen.queryByTestId('account-version')).toBeNull()
  })
})
