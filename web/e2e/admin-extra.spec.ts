// Administration › Audit log and Administration › Backup (asked October
// 30, 2026): the events the console kept, searched and narrowed; a backup
// file with no credential in it, and a restore of the settings from it.
import { readFileSync } from 'node:fs'
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

test('the Audit log page lists what was recorded, searched by words and narrowed by action', async ({ page, request }) => {
  const r = await request.put('/api/v1/settings', { data: { refreshSeconds: 4 }, headers: SAME })
  expect(r.status(), await r.text()).toBe(200)
  await openAdmin(page, 'audit')
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

test('a backup downloads with no credential in it, and a restore of it brings the settings back', async ({ page, request }) => {
  await request.put('/api/v1/settings', { data: { refreshSeconds: 5 }, headers: SAME })
  await openAdmin(page, 'backup')
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByTestId('backup-take').click()])
  expect(download.suggestedFilename()).toMatch(/^221b-backup-.*\.json$/)
  const text = readFileSync((await download.path())!, 'utf8')
  const backup = JSON.parse(text) as { format: string; settings: { refreshSeconds: number }; leftOut: string[]; audit: unknown[] }
  expect(backup.format).toBe('221b-backup/1')
  expect(backup.settings.refreshSeconds).toBe(5)
  expect(backup.leftOut.length).toBeGreaterThan(0)
  expect(text).not.toContain('passwordHash')
  expect(text).not.toMatch(/"token"\s*:/)
  await expect(page.getByTestId('backup-last')).toBeVisible()

  // Changed since, then restored from the file.
  await request.put('/api/v1/settings', { data: { refreshSeconds: 3 }, headers: SAME })
  await page.getByTestId('restore-file').setInputFiles({ name: 'b.json', mimeType: 'application/json', buffer: Buffer.from(text) })
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
