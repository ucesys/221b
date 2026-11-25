import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ToolboxInfo } from '../../api/toolbox'
import { AdminToolbox } from './AdminToolbox'
import type { AdminProps } from '../AdminPage'

const info = vi.hoisted(() => ({ v: null as unknown }))
vi.mock('../../api/toolbox', async (original) => ({
  ...(await original<typeof import('../../api/toolbox')>()),
  fetchToolbox: () => Promise.resolve(info.v),
}))

afterEach(cleanup)

// A console before 0.14 sent an emptied list as null (the toolbox review,
// finding 1): the page shows the values, it does not crash.
describe('Administration › Toolbox', () => {
  it('shows a console whose lists are null', async () => {
    const v: ToolboxInfo = {
      enabled: true,
      readOnly: false,
      production: false,
      twoPerson: false,
      signedIn: true,
      admin: true,
      me: 'Ada',
      off: { 'debug-container': 'off on this console', 'debug-pod': 'off on this console' },
      idleSeconds: 600,
      maxMinutes: 60,
      transcripts: 'none',
      nodeApproval: 'cluster',
      nodeNamespace: '221b-privileged',
      mountVolumes: true,
      fileTransfer: true,
      images: [{ name: 'general', image: 'busybox:1.37' }],
      debugContainerImages: null,
      debugPodProfiles: null,
      nodeProfiles: null,
      namespaces: {},
      nodes: {},
      values: {},
    }
    info.v = v
    const props = { route: { cluster: 'local' }, me: null, now: 0, cluster: null, onToast: () => undefined } as unknown as AdminProps
    render(<AdminToolbox {...props} />)
    expect(await screen.findByTestId('admin-toolbox')).toBeTruthy()
    expect(screen.getByText('general')).toBeTruthy()
  })
})
