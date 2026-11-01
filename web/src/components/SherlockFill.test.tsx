import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SherlockState } from '../api/sherlock'
import { SherlockPane } from './Sherlock'

const state = { status: { enabled: true, available: true }, conv: null } as unknown as SherlockState

afterEach(() => vi.unstubAllGlobals())

describe('a question handed to the pane', () => {
  it('waits in the box and asks nothing until the person sends it', async () => {
    const asked = vi.fn(() => Promise.resolve(new Response('{}')))
    vi.stubGlobal('fetch', asked)
    const filled = vi.fn()
    render(<SherlockPane cluster="prod" clusterName="prod" state={state} onToast={() => undefined} fill="Deficiency review of cluster prod" onFilled={filled} />)
    await waitFor(() => expect(screen.getByTestId<HTMLTextAreaElement>('sherlock-input').value).toBe('Deficiency review of cluster prod'))
    expect(filled).toHaveBeenCalledOnce()
    expect(asked).not.toHaveBeenCalled()
  })
})
