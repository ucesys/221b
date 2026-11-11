import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import { JobsSettings } from './JobsSettings'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchJobsSettings: vi.fn(),
  saveJobsSettings: vi.fn(),
}))

describe('Settings › Jobs', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('saves who may edit jobs and what agents on their own read as', async () => {
    vi.mocked(api.fetchJobsSettings).mockResolvedValue({ readerRole: 'viewer', readerGroup: 'ops' })
    vi.mocked(api.saveJobsSettings).mockImplementation(async (s) => ({ ...s, setBy: 'sam' }))
    render(<JobsSettings />)
    const save = (await screen.findByTestId('jobs-settings-save')) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    expect((screen.getByTestId('jobs-reader-group') as HTMLInputElement).value).toBe('ops')
    fireEvent.click(screen.getByTestId('jobs-editor-admin'))
    fireEvent.change(screen.getByTestId('jobs-reader-role'), { target: { value: 'superViewer' } })
    expect(screen.queryByTestId('jobs-reader-group')).toBeNull()
    fireEvent.click(save)
    await waitFor(() => expect(api.saveJobsSettings).toHaveBeenCalledWith({ editors: ['admin'], readerRole: 'superViewer', readerGroup: 'ops' }))
    expect(await screen.findByText('Saved.')).toBeTruthy()
  })
})
