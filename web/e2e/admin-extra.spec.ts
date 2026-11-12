// Administration › Audit log and Administration › Backup (asked October
// 30, 2026): the events the console kept, searched and narrowed; a backup
// file with no credential in clear in it, and a restore of the settings
// from it; a backup sealed with another key (ADR-137).
import { readFileSync } from 'node:fs'
import { gunzipSync } from 'node:zlib'
import type { Page } from '@playwright/test'
import { expect, test } from './test'

const SAME = { 'Sec-Fetch-Site': 'same-origin' }

async function openAdmin(page: Page, which: string) {
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.getByTestId('admin-link').click()
  await page.locator(`[data-admin-page=${which}]`).click()
}

test.afterEach(async ({ request }) => {
  await request.put('/api/v1/settings', { data: { refreshSeconds: 3 }, headers: SAME })
})

test('Logs › Audit log lists what was recorded, searched by words and narrowed by action', async ({ page, request }) => {
  const r = await request.put('/api/v1/settings', { data: { refreshSeconds: 4 }, headers: SAME })
  expect(r.status(), await r.text()).toBe(200)
  await openAdmin(page, 'logs')
  await expect(page.getByTestId('logs-sessions')).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('logs-audit').click()
  await expect(page).toHaveURL(/#\/admin\/logs\/audit/)
  const audit = page.getByTestId('admin-audit')
  await expect(audit).toBeVisible()
  const table = audit.getByTestId('audit-table')
  await expect(table.locator('tr[data-action="settings.update"]').first()).toBeVisible()
  await audit.getByTestId('audit-search').fill('settings')
  await expect(table.locator('tbody tr').first()).toContainText('settings')
  await audit.getByTestId('audit-search').fill('nothing-like-this-zzz')
  await expect(audit).toContainText('No event matches.')
  await audit.getByTestId('audit-search').fill('')
  await audit.getByTestId('audit-action').selectOption('settings.update')
  await expect(table.locator('tbody tr').first()).toBeVisible()
  await expect(table.locator('tbody tr[data-action]:not([data-action="settings.update"])')).toHaveCount(0)
  await audit.getByTestId('audit-outcome').selectOption('denied')
  await audit.getByTestId('audit-action').selectOption('')
  await expect(table.locator('tbody tr[data-outcome="ok"]')).toHaveCount(0)
})

test('a backup downloads gzipped with each secret sealed or a placeholder, and a restore of it brings the settings back', async ({ page, request }) => {
  await request.put('/api/v1/settings', { data: { refreshSeconds: 5 }, headers: SAME })
  await openAdmin(page, 'backup')
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByTestId('backup-take').click()])
  expect(download.suggestedFilename()).toMatch(/^221b-backup-.*\.json\.gz$/)
  const gz = readFileSync((await download.path())!)
  const text = gunzipSync(gz).toString('utf8')
  const backup = JSON.parse(text) as { format: string; settings: { refreshSeconds: number }; leftOut: string[]; audit: unknown[] }
  expect(backup.format).toBe('221b-backup/2')
  expect(backup.settings.refreshSeconds).toBe(5)
  expect(backup.leftOut.length).toBeGreaterThan(0)
  expect(text).not.toContain('passwordHash')
  // A secret's place holds a sealed value or the placeholder, never a
  // value in clear (ADR-137).
  expect(text).not.toMatch(/"(token|kubeconfig|keyPem|oidcClientSecret|oidcRefreshToken|repoToken|password)"\s*:\s*"(?!<secret: not in the backup>"|<sealed:[0-9a-f]{32}:)/)
  await expect(page.getByTestId('backup-key')).toBeVisible()
  await expect(page.getByTestId('backup-last')).toBeVisible()

  // Changed since, then restored from the file.
  await request.put('/api/v1/settings', { data: { refreshSeconds: 3 }, headers: SAME })
  await page.getByTestId('restore-file').setInputFiles({ name: 'b.json.gz', mimeType: 'application/gzip', buffer: gz })
  await expect(page.getByTestId('restore-preview')).toBeVisible()
  await expect(page.getByTestId('restore-part-settings')).toBeChecked()
  await page.getByTestId('restore-start').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('restore-report')).toContainText('Settings: 1')
  const after = (await (await request.get('/api/v1/settings')).json()) as { refreshSeconds: number }
  expect(after.refreshSeconds).toBe(5)
  // The backup and the restore are in the audit log.
  const audit = (await (await request.get('/api/v1/admin/audit?action=backup')).json()) as { events: { action: string }[] }
  expect(audit.events.map((e) => e.action)).toEqual(expect.arrayContaining(['backup.export', 'backup.restore']))

  // A file that is not a backup.
  await page.getByTestId('restore-file').setInputFiles({ name: 'x.json', mimeType: 'application/json', buffer: Buffer.from('{"format":"nope"}') })
  await expect(page.getByTestId('restore-error')).toContainText('not a 221B backup')
})

// ADR-137: a backup whose secrets are sealed with another install's key
// is restored without them only on a yes; the report lists each secret
// with where to set it.
test('a backup sealed with another key asks first, restores the rest and lists the secrets to set again', async ({ page, request }) => {
  const other = 'b'.repeat(32)
  const id = 'e2e-sealed'
  const file = {
    format: '221b-backup/2',
    version: 'e2e',
    taken: '2026-11-09T10:00:00Z',
    leftOut: [],
    secrets: { cipher: 'aes-256-gcm', key: other },
    connections: [{ id, name: 'e2e-sealed', env: 'development', server: 'https://10.255.0.9:6443', ca: 'skip', auth: 'token', mode: 'read', credential: { token: `<sealed:${other}:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA>` } }],
  }
  try {
    await openAdmin(page, 'backup')
    await page.getByTestId('restore-file').setInputFiles({ name: 'sealed.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(file)) })
    await expect(page.getByTestId('restore-sealing')).toHaveAttribute('data-sealing', 'other')
    await expect(page.getByTestId('restore-part-connections')).toBeChecked()
    await page.getByTestId('restore-start').click()
    await expect(page.getByTestId('restore-without-secrets')).toContainText('Restore the rest without the secrets?')
    await expect(page.getByTestId('confirm-ok')).toHaveText('Restore without the secrets')
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByTestId('restore-report')).toContainText('Cluster connections: 1')
    await expect(page.getByTestId('restore-secret').first()).toContainText('the token of the connection e2e-sealed')
    await expect(page.getByTestId('restore-secret').first()).toContainText('Clusters › e2e-sealed › Edit connection')
    // The server refuses the same restore without the yes.
    const refused = await request.post('/api/v1/admin/restore', { data: { backup: file, parts: { connections: true } }, headers: SAME })
    expect(refused.status()).toBe(409)
    expect(await refused.text()).toContain('backup-key')
  } finally {
    await request.delete(`/api/v1/clusters/${id}`, { headers: SAME })
  }
})

test("a role's See every permission opens Permissions on that role alone, and Full reads Elevated", async ({ page }) => {
  await page.goto('/#/admin/roles/admin', { waitUntil: 'domcontentloaded' })
  const drawer = page.getByTestId('role-drawer')
  await expect(drawer).toBeVisible()
  await expect(drawer).not.toContainText('Full definition')
  await drawer.getByTestId('role-permissions').click()
  await expect(page).toHaveURL(/#\/admin\/permissions\?role=admin/)
  await expect(page.getByTestId('role-filter-admin')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('role-filter-clear')).toBeVisible()
  await expect(page.locator('[data-role-summary]').first()).not.toContainText(/\bFull\b/)
})
