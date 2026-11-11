import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { gzipSync } from 'node:zlib'
import { BACKUP_FORMAT, backupSealing, partCount, readBackup, RESTORE_PARTS, type BackupFile } from '../api/admin'
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

describe('reading a backup file', () => {
  const b = {
    format: BACKUP_FORMAT,
    version: 'x',
    taken: '2026-11-04T10:00:00Z',
    leftOut: [],
    connections: [
      { id: 'local', name: 'Here', server: 'https://h', auth: 'incluster', local: true },
      { id: 'prod', name: 'Prod', server: 'https://p', auth: 'token', credential: { token: '<secret: not in the backup>' } },
    ],
  }
  it('reads one gzipped, as the console writes it, and one of the first format in plain JSON', async () => {
    const gz = await readBackup(new File([gzipSync(JSON.stringify(b))], 'b.json.gz'))
    expect(gz.connections?.[1]?.credential?.token).toBe('<secret: not in the backup>')
    expect(partCount(gz, 'connections')).toBe(1)
    const v1 = await readBackup(new File([JSON.stringify({ ...b, format: '221b-backup/1' })], 'b.json'))
    expect(v1.format).toBe('221b-backup/1')
  })
  it('says what is wrong with a file that is not one', async () => {
    await expect(readBackup(new File([gzipSync('{"format":"nope"}')], 'x.gz'))).rejects.toThrow('This is not a 221B backup: its format is "nope"')
    await expect(readBackup(new File([new Uint8Array([0x1f, 0x8b, 1, 2, 3])], 'x.gz'))).rejects.toThrow('damaged or cut short')
    await expect(readBackup(new File(['not json'], 'x.json'))).rejects.toThrow('neither gzipped JSON nor JSON')
  })
  it('says connections come back waiting for their credentials', () => {
    expect(RESTORE_PARTS.find((p) => p.id === 'connections')?.how).toContain('wait for their credentials')
  })
})

// ADR-137: a backup's secrets are sealed with the install's key; a file
// sealed with another key is restored without them only on a yes, and the
// report lists each secret with where to set it.
describe('restoring a backup sealed with a backup key', () => {
  const HERE = 'a'.repeat(32)
  const sealed = (key: string): BackupFile => ({
    format: BACKUP_FORMAT,
    version: 'x',
    taken: '2026-11-09T10:00:00Z',
    leftOut: [],
    secrets: { cipher: 'aes-256-gcm', key },
    connections: [{ id: 'prod', name: 'Prod', server: 'https://p', auth: 'token', credential: { token: `<sealed:${key}:AAAA>` } }],
  })
  const report = { restored: { connections: 1 }, notes: [], secrets: [{ part: 'Cluster connections', name: 'the token of the connection Prod', where: 'Clusters › Prod › Edit connection' }] }
  // The server: the key's fingerprint, and a restore that answers 409
  // until withoutSecrets is sent, when refuse is true.
  const serve = (refuse: boolean) => {
    const posted: { withoutSecrets?: boolean }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url === '/api/v1/admin/backup/parts') return new Response(JSON.stringify({ parts: [], key: { fingerprint: HERE, secret: '221b-backup-key' } }), { status: 200 })
        if (url === '/api/v1/admin/restore') {
          const body = JSON.parse(String(init?.body)) as { withoutSecrets?: boolean }
          posted.push(body)
          if (refuse && !body.withoutSecrets) return new Response(JSON.stringify({ error: 'sealed with another key', code: 'backup-key' }), { status: 409 })
          return new Response(JSON.stringify(report), { status: 200 })
        }
        return new Response('{}', { status: 404 })
      }),
    )
    return posted
  }
  const choose = (b: BackupFile) => {
    const input = screen.getByTestId('restore-file') as HTMLInputElement
    Object.defineProperty(input, 'files', { configurable: true, value: [new File([JSON.stringify(b)], 'b.json', { type: 'application/json' })] })
    fireEvent.change(input)
  }
  afterEach(() => vi.unstubAllGlobals())

  it('names the key by fingerprint and its Secret, never the key', async () => {
    serve(false)
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    await waitFor(() => expect(screen.getByTestId('backup-key').textContent).toContain('221b-backup-key'))
    expect(screen.getByTestId('backup-key').textContent).toContain(HERE.slice(0, 16))
    expect(screen.getByTestId('backup-key').textContent).toContain('cannot be recovered')
  })

  it('asks whether to restore without the secrets when they are sealed with another key, and sends the yes', async () => {
    const posted = serve(true)
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    await waitFor(() => screen.getByTestId('backup-key'))
    choose(sealed('b'.repeat(32)))
    await waitFor(() => expect(screen.getByTestId('restore-sealing').dataset.sealing).toBe('other'))
    fireEvent.click(screen.getByTestId('restore-start'))
    expect(screen.getByTestId('restore-without-secrets').textContent).toContain('Restore the rest without the secrets?')
    expect(screen.getByTestId('confirm-ok').textContent).toBe('Restore without the secrets')
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => screen.getByTestId('restore-report'))
    expect(posted).toHaveLength(1)
    expect(posted[0]?.withoutSecrets).toBe(true)
  })

  it('lists every secret to set again with where to set it', async () => {
    serve(true)
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    await waitFor(() => screen.getByTestId('backup-key'))
    choose(sealed('b'.repeat(32)))
    await waitFor(() => screen.getByTestId('restore-start'))
    fireEvent.click(screen.getByTestId('restore-start'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    const items = await screen.findAllByTestId('restore-secret')
    expect(items).toHaveLength(1)
    expect(items[0]?.textContent).toContain('the token of the connection Prod')
    expect(items[0]?.textContent).toContain('Clusters › Prod › Edit connection')
  })

  it('restores with the same key without asking, and asks when the server says the key differs', async () => {
    const posted = serve(false)
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    await waitFor(() => screen.getByTestId('backup-key'))
    choose(sealed(HERE))
    await waitFor(() => expect(screen.getByTestId('restore-sealing').dataset.sealing).toBe('same'))
    fireEvent.click(screen.getByTestId('restore-start'))
    expect(screen.queryByTestId('restore-without-secrets')).toBeNull()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => screen.getByTestId('restore-report'))
    expect(posted[0]?.withoutSecrets).toBeUndefined()
    cleanup()

    // The key changed since the page loaded: the server's 409 asks.
    const posted2 = serve(true)
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    await waitFor(() => screen.getByTestId('backup-key'))
    choose(sealed(HERE))
    await waitFor(() => screen.getByTestId('restore-start'))
    fireEvent.click(screen.getByTestId('restore-start'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => screen.getByTestId('restore-without-secrets'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => screen.getByTestId('restore-report'))
    expect(posted2.map((p) => p.withoutSecrets)).toEqual([undefined, true])
  })

  it('says a backup of placeholders lists its secrets afterwards', async () => {
    serve(false)
    render(<AdminBackup {...({ onToast: vi.fn() } as unknown as AdminProps)} />)
    await waitFor(() => screen.getByTestId('backup-key'))
    choose({ ...sealed(HERE), secrets: undefined })
    await waitFor(() => expect(screen.getByTestId('restore-sealing').dataset.sealing).toBe('placeholders'))
    expect(backupSealing({ ...sealed(HERE), secrets: undefined }, { fingerprint: HERE })).toBe('placeholders')
    expect(backupSealing(sealed(HERE), {})).toBe('other')
  })
})
