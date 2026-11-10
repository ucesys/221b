// A case's type and incident number, and who did it (Phase 12, wave 6c,
// CF-35 to CF-37): New Case picks the type (Other unless picked) and takes
// an incident number; the drawer's header shows both, the footer changes
// the type, Edit changes or clears the incident number, and the history
// names who did each thing with what they are. The Inbox shows Type,
// Incident and Last change columns, filters by type in the address and
// finds a case by its incident number. The bar sets a type and an
// incident number on many cases at once. The issue API takes both.
// Written against the Contract of wave 6c; not yet run against its server.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }

interface Brief {
  id: string
  number: number
  status: string
  type?: string
  incident?: string
  history?: { what: string; by?: { name: string; kind: string } }[]
}

async function read(page: Page, id: string | number): Promise<Brief> {
  const res = await page.request.get(`/api/v1/cases/cases/${id}`)
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as Brief
}

const stamp = Date.now()
const tag = `typed-${stamp}`
const INC = `INC${stamp}`

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  const list = (await (await page.request.get(`/api/v1/cases/cases?q=${encodeURIComponent(tag)}`)).json()) as { cases: Brief[] }
  for (const c of list.cases.filter((x) => x.status !== 'closed')) await page.request.post(`/api/v1/cases/cases/${c.id}/close`, { headers: SAME, data: { outcome: 'noAction', explanation: 'e2e: done' } })
  await page.close()
})

test('New Case takes a type and an incident number; the drawer shows them, changes the type and clears the number; the history says who (CF-35 to CF-37)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect((await page.request.put('/api/v1/jobs/case-analysis', { headers: SAME, data: { on: false } })).status()).toBe(200)

  await page.goto('/#/cases/inbox')
  await page.getByTestId('cases-new-case').click()
  await page.getByTestId('case-new-title').fill(`${tag} anyone can read the secrets of shop`)
  // Other unless picked.
  await expect(page.getByTestId('case-new-type')).toHaveValue('other')
  await page.getByTestId('case-new-type').selectOption('security')
  await page.getByTestId('case-new-incident').fill(INC)
  await page.getByTestId('case-new-open').click()

  const drawer = page.getByTestId('case-drawer')
  // Those who work the case set the type in the header (CF-58).
  await expect(drawer.getByTestId('case-type-set')).toHaveValue('security')
  await expect(drawer.getByTestId('case-incident')).toHaveText(INC)
  await expect(drawer.getByTestId('case-created-by')).toContainText('(person)')
  const id = (await drawer.getByTestId('case-kicker').textContent())!.match(/C-\d+/)![0]
  expect(await read(page, id)).toMatchObject({ type: 'security', incident: INC })

  // The header changes the type; the history says who, a person.
  await drawer.getByTestId('case-type-set').selectOption('compliance')
  await expect(drawer.getByTestId('case-type-set')).toHaveValue('compliance')
  await expect(drawer.getByTestId('case-history-by').first()).toContainText('(person)')
  expect((await read(page, id)).type).toBe('compliance')

  // Edited in place, the incident number is cleared when emptied, and set again (CF-60).
  await drawer.getByTestId('case-incident-edit').click()
  await expect(drawer.getByTestId('case-incident-input')).toHaveValue(INC)
  await drawer.getByTestId('case-incident-input').fill('')
  await drawer.getByTestId('case-incident-input').press('Enter')
  await expect(drawer.getByTestId('case-incident')).toHaveCount(0)
  expect((await read(page, id)).incident ?? '').toBe('')
  await drawer.getByTestId('case-incident-edit').click()
  await drawer.getByTestId('case-incident-input').fill(INC)
  await drawer.getByTestId('case-incident-input').press('Enter')
  await expect(drawer.getByTestId('case-incident')).toHaveText(INC)
})

test('the Inbox shows Type, Incident and Last change, filters by type and finds a case by its incident number (CF-35 to CF-37)', async ({ page }) => {
  await page.goto(`/#/cases/inbox?q=${encodeURIComponent(INC)}`)
  const row = page.locator('[data-inbox-row^="case:"]').filter({ hasText: tag })
  await expect(row).toHaveCount(1)
  await expect(row.getByTestId('inbox-row-type')).toHaveText('Compliance')
  await expect(row.getByTestId('inbox-row-incident')).toHaveText(INC)
  // A person changed it last, a moment ago.
  await expect(row.getByTestId('inbox-row-last')).toHaveText(/ · \d+(s|m) ago$/)
  await expect(row.getByTestId('inbox-row-last')).not.toContainText('(agent)')
  // The type filter, in the address.
  await page.getByTestId('inbox-type').selectOption('compliance')
  await expect(page).toHaveURL(/type=compliance/)
  await expect(row).toHaveCount(1)
  await page.getByTestId('inbox-type').selectOption('capacity')
  await expect(page).toHaveURL(/type=capacity/)
  await expect(row).toHaveCount(0)
  await page.getByTestId('inbox-type').selectOption('')
  await expect(row).toHaveCount(1)
})

test('the bar sets one type and one incident number on many cases (CF-38)', async ({ page }) => {
  await page.goto('/#/nodes')
  const made: string[] = []
  for (const n of [1, 2]) {
    const res = await page.request.post('/api/v1/cases/cases', { headers: SAME, data: { title: `${tag}-many ${n}: pods evicted on worker-${n}`, priority: 3 } })
    expect(res.status(), await res.text()).toBe(201)
    made.push(((await res.json()) as Brief).id)
  }
  await page.goto(`/#/cases/inbox?q=${encodeURIComponent(`${tag}-many`)}`)
  for (const id of made) await page.locator(`[data-testid="inbox-pick"][data-id="${id}"]`).check()
  await expect(page.getByTestId('inbox-bulk-count')).toHaveText('2 selected')

  await page.getByTestId('inbox-bulk-type').click()
  await page.getByTestId('confirm').getByTestId('inbox-bulk-type-pick').selectOption('capacity')
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  await expect(page.getByTestId('inbox-bulk-results').locator('[data-ok="true"]')).toHaveCount(2)
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  for (const id of made) expect((await read(page, id)).type).toBe('capacity')

  for (const id of made) await page.locator(`[data-testid="inbox-pick"][data-id="${id}"]`).check()
  await page.getByTestId('inbox-bulk-incident').click()
  await page.getByTestId('confirm').getByTestId('inbox-bulk-incident-text').fill(`${INC}-B`)
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  await expect(page.getByTestId('inbox-bulk-results').locator('[data-ok="true"]')).toHaveCount(2)
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  for (const id of made) expect((await read(page, id)).incident).toBe(`${INC}-B`)
  const rows = page.locator('[data-inbox-row^="case:"]').filter({ hasText: `${tag}-many` })
  await expect(rows.getByTestId('inbox-row-type')).toHaveText(['Capacity', 'Capacity'])
  await expect(rows.getByTestId('inbox-row-incident')).toHaveText([`${INC}-B`, `${INC}-B`])
})

test('the issue API takes an incident number and a type (CF-36, CF-37)', async ({ page }) => {
  await page.goto('/#/nodes')
  const res = await page.request.post('/api/issues/v1/reports', { headers: SAME, data: { text: `${tag} the shop is slow since 9:00`, for: { id: 'dev', name: 'Dev Person' }, incident: `${INC}-API`, type: 'performance' } })
  expect(res.status(), await res.text()).toBe(201)
  const n = ((await res.json()) as { report: { number: number } }).report.number
  expect(await read(page, n)).toMatchObject({ incident: `${INC}-API`, type: 'performance' })
  await page.goto(`/#/cases/inbox/case/${n}`)
  await expect(page.getByTestId('case-drawer').getByTestId('case-type-set')).toHaveValue('performance')
  await expect(page.getByTestId('case-drawer').getByTestId('case-incident')).toHaveText(`${INC}-API`)
})
