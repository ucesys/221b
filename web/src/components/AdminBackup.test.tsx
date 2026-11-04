import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { BACKUP_FORMAT, partCount, RESTORE_PARTS, type BackupFile } from '../api/admin'
import { AdminBackup } from './AdminBackup'
import type { AdminProps } from './AdminPage'

afterEach(cleanup)

describe('choosing a backup file', () => {
  it('empties the input, so the same file chosen again counts as a change', async () => {
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    const input = screen.getByTestId('restore-file') as HTMLInputElement
    const cleared = vi.fn()
    Object.defineProperty(input, 'value', { configurable: true, get: () => 'C:\\fakepath\\b.json', set: cleared })
    const file = new File(['{"format":"nope"}'], 'b.json', { type: 'application/json' })
    Object.defineProperty(input, 'files', { configurable: true, value: [file] })
    fireEvent.change(input)
    expect(cleared).toHaveBeenCalledWith('')
    // The file was still read.
    await waitFor(() => expect(document.body.textContent).toContain('This is not a 221B backup'))
  })
})

describe('the Automation part of a backup', () => {
  it('counts scripts, run profiles, groups and fleets with parameters, and says scripts come back as drafts', () => {
    const b: BackupFile = {
      format: BACKUP_FORMAT,
      version: 'x',
      taken: '2026-10-30T10:00:00Z',
      leftOut: [],
      automation: { scripts: [{}, {}], runProfiles: [{}], groups: [{}], variables: { local: { _all: {}, web: {} } } },
    }
    expect(partCount(b, 'automation')).toBe(6)
    expect(partCount({ ...b, automation: undefined }, 'automation')).toBe(0)
    expect(RESTORE_PARTS.find((p) => p.id === 'automation')?.how).toContain('drafts')
  })
})
