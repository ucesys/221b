import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { BACKUP_FORMAT, describeSchedule, fetchBackupFromS3, fetchBackupS3, partCount, restoreBackup, restoreParts, runBackupS3, saveBackupS3, testBackupS3, type BackupFile, type S3View } from '../api/admin'
import { AdminBackup } from './AdminBackup'
import { AdminBackupS3, runLine } from './AdminBackupS3'
import type { AdminProps } from './AdminPage'

const VIEW: S3View = {
  settings: { enabled: true, bucket: 'backups', prefix: 'prod/', region: 'eu-central-1', credentials: 'keys', schedule: { kind: 'daily', at: '03:00', timeZone: 'Europe/Berlin' }, retention: { mode: 'count', count: 14 }, sse: 'AES256' },
  saved: true,
  keys: { accessKey: false, passphrase: false },
  identity: false,
  schedule: 'Every day at 03:00, Europe/Berlin',
  next: '2026-11-06T02:00:00Z',
  running: false,
  status: {
    recent: [
      { started: '2026-11-05T02:00:00Z', by: 'scheduled backup', ok: false, error: 'putting prod/x in backups: S3 answered 403 AccessDenied' },
      { started: '2026-11-04T02:00:00Z', by: 'scheduled backup', ok: true, key: 'prod/221b-backup-2026-11-04-020000.json.gz', size: 2048 },
    ],
  },
}

const FILE: BackupFile = { format: BACKUP_FORMAT, version: '0.12.0', taken: '2026-11-04T02:00:00Z', leftOut: [], settings: { refreshSeconds: 5 }, jobs: [{ name: 'nightly' }] }

vi.mock('../api/admin', async (orig) => {
  const real = await orig<typeof import('../api/admin')>()
  return {
    ...real,
    fetchBackupS3: vi.fn(async () => VIEW),
    saveBackupS3: vi.fn(async (settings: S3View['settings']) => ({ ...VIEW, settings, keys: { accessKey: true, passphrase: false } })),
    testBackupS3: vi.fn(async () => [
      { name: 'Credentials', ok: true, detail: 'the access key saved' },
      { name: 'Put a test object', ok: false, detail: 'S3 answered 403 AccessDenied' },
    ]),
    runBackupS3: vi.fn(async () => ({ started: '2026-11-05T10:00:00Z', by: 'ops@corp.example', ok: true, key: 'prod/221b-backup-2026-11-05-100000.json.gz', size: 1000 })),
    describeSchedule: vi.fn(async (s: { kind: string }) => (s.kind === 'cron' ? { error: 'the cron expression "x" needs five fields' } : { description: `Words for ${s.kind}`, next: ['2026-11-06T02:00:00Z'] })),
    listBackupsS3: vi.fn(async () => [{ key: 'prod/221b-backup-2026-11-04-020000.json.gz.enc', size: 4096, modified: '2026-11-04T02:00:05Z', taken: '2026-11-04T02:00:00Z', encrypted: true }]),
    fetchBackupFromS3: vi.fn(async () => FILE),
    fetchBackupMeta: vi.fn(async () => ({
      parts: [
        { name: 'settings', label: 'Settings', restorable: true },
        { name: 'jobs', label: 'Jobs', how: 'are added or replaced, by name', restorable: true },
        { name: 'chats', label: 'Chat conversations', optional: true, restorable: false },
      ],
      key: {},
    })),
    restoreBackup: vi.fn(async () => ({ restored: { jobs: 1 }, notes: [] })),
  }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const props = { onToast: vi.fn() } as unknown as AdminProps
const type = (id: string, value: string) => fireEvent.change(screen.getByTestId(id), { target: { value } })

describe('the scheduled backups to S3', () => {
  it('shows every row of the design: destination, credentials, schedule, retention, encryption, status and restore', async () => {
    render(<AdminBackupS3 onToast={vi.fn()} onRestore={vi.fn()} />)
    await screen.findByTestId('admin-backup-s3')
    for (const row of ['s3-destination', 's3-credentials', 's3-schedule', 's3-retention', 's3-encryption', 's3-status', 's3-restore']) expect(screen.getByTestId(row)).toBeTruthy()
    expect((screen.getByTestId('s3-bucket') as HTMLInputElement).value).toBe('backups')
    expect(screen.getByTestId('s3-key-state').textContent).toBe('No access key')
    expect(screen.getByTestId('s3-last').textContent).toBe('Last backup failed')
    expect(screen.getByTestId('s3-failure').textContent).toContain('403 AccessDenied')
    expect(screen.getByTestId('s3-next').textContent).toContain('Every day at 03:00, Europe/Berlin')
    await waitFor(() => expect(screen.getByTestId('s3-sched-words').textContent).toContain('Words for daily'))
  })

  it('sends a key only once Replace… is pressed, and never shows one back', async () => {
    render(<AdminBackupS3 onToast={vi.fn()} onRestore={vi.fn()} />)
    await screen.findByTestId('s3-save')
    fireEvent.click(screen.getByTestId('s3-save'))
    await waitFor(() => expect(saveBackupS3).toHaveBeenCalledTimes(1))
    expect(vi.mocked(saveBackupS3).mock.calls[0]![1]).toEqual({})
    fireEvent.click(screen.getByTestId('s3-key-replace'))
    type('s3-key-id', 'AKIAEXAMPLE')
    type('s3-key-secret', 'very-secret')
    type('s3-bucket', 'other-bucket')
    fireEvent.click(screen.getByTestId('s3-save'))
    await waitFor(() => expect(saveBackupS3).toHaveBeenCalledTimes(2))
    const [settings, keys] = vi.mocked(saveBackupS3).mock.calls[1]!
    expect(settings.bucket).toBe('other-bucket')
    expect(keys).toEqual({ accessKeyId: 'AKIAEXAMPLE', secretAccessKey: 'very-secret' })
    await waitFor(() => expect(screen.getByTestId('s3-key-state').textContent).toBe('access key saved'))
    expect(screen.queryByTestId('s3-key-secret')).toBeNull()
    expect(document.body.textContent).not.toContain('very-secret')
  })

  it('edits the schedule in its shapes, and says what is wrong with a cron expression', async () => {
    render(<AdminBackupS3 onToast={vi.fn()} onRestore={vi.fn()} />)
    await screen.findByTestId('s3-sched-kind')
    fireEvent.change(screen.getByTestId('s3-sched-kind'), { target: { value: 'weekly' } })
    fireEvent.click(screen.getByTestId('s3-day-fri'))
    await waitFor(() => expect(describeSchedule).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'weekly', days: ['mon', 'fri'], at: '03:00', timeZone: 'Europe/Berlin' })))
    fireEvent.change(screen.getByTestId('s3-sched-kind'), { target: { value: 'hours' } })
    type('s3-sched-every', '6')
    expect(screen.getByTestId('s3-sched-at')).toBeTruthy()
    fireEvent.change(screen.getByTestId('s3-sched-kind'), { target: { value: 'cron' } })
    type('s3-sched-cron', 'x')
    await waitFor(() => expect(screen.getByTestId('s3-sched-error').textContent).toContain('five fields'))
  })

  it('tests the connection step by step, and runs a backup now', async () => {
    render(<AdminBackupS3 onToast={vi.fn()} onRestore={vi.fn()} />)
    fireEvent.click(await screen.findByTestId('s3-test'))
    await waitFor(() => expect(screen.getByTestId('s3-steps').textContent).toContain('✗ Put a test object — S3 answered 403 AccessDenied'))
    expect(testBackupS3).toHaveBeenCalledWith(expect.objectContaining({ bucket: 'backups' }), {})
    fireEvent.click(screen.getByTestId('s3-run'))
    await waitFor(() => expect(runBackupS3).toHaveBeenCalled())
    await waitFor(() => expect(fetchBackupS3).toHaveBeenCalledTimes(2))
  })

  it('asks for a passphrase when the files are encrypted on the console, and for a KMS key with SSE-KMS', async () => {
    render(<AdminBackupS3 onToast={vi.fn()} onRestore={vi.fn()} />)
    await screen.findByTestId('s3-encrypt')
    expect(screen.queryByTestId('s3-pass-state')).toBeNull()
    fireEvent.click(screen.getByTestId('s3-encrypt'))
    expect(screen.getByTestId('s3-pass-state').textContent).toBe('No passphrase')
    fireEvent.click(screen.getByTestId('s3-pass-replace'))
    type('s3-passphrase', 'a long passphrase here')
    fireEvent.change(screen.getByTestId('s3-sse'), { target: { value: 'aws:kms' } })
    type('s3-kms', 'alias/backups')
    fireEvent.click(screen.getByTestId('s3-save'))
    await waitFor(() => expect(saveBackupS3).toHaveBeenCalled())
    const [settings, keys] = vi.mocked(saveBackupS3).mock.calls[0]!
    expect(settings).toMatchObject({ encrypt: true, sse: 'aws:kms', kmsKeyId: 'alias/backups' })
    expect(keys).toEqual({ passphrase: 'a long passphrase here' })
  })

  it('lists the backups in the bucket and hands one to the restore', async () => {
    const onRestore = vi.fn()
    render(<AdminBackupS3 onToast={vi.fn()} onRestore={onRestore} />)
    fireEvent.click(await screen.findByTestId('s3-list'))
    await waitFor(() => expect(screen.getByTestId('s3-backups').textContent).toContain('(encrypted)'))
    fireEvent.click(screen.getByTestId('s3-restore-pick'))
    await waitFor(() => expect(onRestore).toHaveBeenCalledWith('221b-backup-2026-11-04-020000.json.gz.enc', FILE, 's3://backups/prod/221b-backup-2026-11-04-020000.json.gz.enc'))
    expect(fetchBackupFromS3).toHaveBeenCalledWith('prod/221b-backup-2026-11-04-020000.json.gz.enc')
  })

  it('says a run in a line', () => {
    expect(runLine({ started: '2026-11-05T02:00:00Z', by: 'scheduled backup', ok: true, key: 'k', size: 2048, pruned: 2 })).toContain('on schedule: k')
    expect(runLine({ started: '2026-11-05T02:00:00Z', slot: '2026-11-05T02:00:00Z', by: 'scheduled backup', ok: false, missed: true, error: 'missed: the console was not running' })).toContain('missed: the console was not running')
  })
})

describe('a restore from the bucket', () => {
  it('offers the same parts as a file, another package’s part among them, and names where it came from', async () => {
    render(<AdminBackup {...props} />)
    fireEvent.click(await screen.findByTestId('s3-list'))
    fireEvent.click(await screen.findByTestId('s3-restore-pick'))
    await waitFor(() => expect(screen.getByTestId('restore-preview').textContent).toContain('221b-backup-2026-11-04-020000.json.gz.enc'))
    expect(screen.getByTestId('restore-part-jobs')).toBeTruthy()
    expect((screen.getByTestId('restore-part-jobs') as HTMLInputElement).checked).toBe(true)
    expect(screen.getByTestId('backup-optional').parentElement?.textContent).toContain('chat conversations')
    fireEvent.click(screen.getByTestId('restore-start'))
    fireEvent.click(await screen.findByText('Restore', { selector: 'button' }))
    await waitFor(() => expect(restoreBackup).toHaveBeenCalledWith(FILE, ['settings', 'jobs'], 's3://backups/prod/221b-backup-2026-11-04-020000.json.gz.enc', false))
    await waitFor(() => expect(screen.getByTestId('restore-report').textContent).toContain('Jobs: 1'))
  })

  it('counts a part it does not know, and names it', () => {
    const parts = restoreParts([], { ...FILE, reports: { a: {}, b: {} } })
    expect(parts.find((p) => p.id === 'reports')?.how).toContain('does not know')
    expect(partCount({ ...FILE, reports: { a: {}, b: {} } }, 'reports')).toBe(2)
    expect(partCount(FILE, 'jobs')).toBe(1)
  })
})
