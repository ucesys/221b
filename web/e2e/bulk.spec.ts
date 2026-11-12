// Many cases at once (Phase 12, wave 6c, CF-38): in AIOps › Inbox the
// cases a search lists are picked with the heading's box, the bar says how
// many, and Close closes them all with one outcome. Each case is checked
// by the rules of its own Close: a fix at P1 without an RCA is refused and
// stays picked, the others close; the refused one then closes as no fix
// needed. Fixed with one RCA (wave 6e): its fix is what was done, it is
// kept on each case, each names the others closed with it, and the Inbox
// finds them by its cause. A duplicate of one open case closes many at once. The bulk route
// refuses an empty list and more than 100 cases. Written against the
// Contract of wave 6c; not yet run against its server.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }

interface Brief {
  id: string
  number: number
  status: string
  outcome?: string
  duplicateOf?: string
}

/** Opens a case by hand through the API. */
async function open(page: Page, title: string, more: Record<string, unknown> = {}): Promise<{ id: string; number: number }> {
  const res = await page.request.post('/api/v1/aiops/cases', { headers: SAME, data: { title, priority: 3, ...more } })
  expect(res.status(), await res.text()).toBe(201)
  return (await res.json()) as { id: string; number: number }
}

async function read(page: Page, id: string): Promise<Brief> {
  const res = await page.request.get(`/api/v1/aiops/cases/${id}`)
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as Brief
}

/** The cases a search lists, by their ids, in the table's order. */
async function listed(page: Page): Promise<string[]> {
  return page.locator('[data-testid="inbox-pick"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-id') ?? ''))
}

const tag = `bulk-${Date.now()}`

test.afterAll(async ({ browser }) => {
  // What this spec opened and left open is closed again, so other specs see the Inbox as before.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  const list = (await (await page.request.get(`/api/v1/aiops/cases?q=${encodeURIComponent(tag)}`)).json()) as { cases: Brief[] }
  for (const c of list.cases.filter((x) => x.status !== 'closed')) await page.request.post(`/api/v1/aiops/cases/${c.id}/close`, { headers: SAME, data: { outcome: 'noAction', explanation: 'e2e: done' } })
  await page.close()
})

test('closes the picked cases with one outcome; a case the outcome does not fit is refused, says why and stays picked (CF-38)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  // The case analysis job off: nothing analyses these meanwhile.
  expect((await page.request.put('/api/v1/jobs/case-analysis', { headers: SAME, data: { on: false } })).status()).toBe(200)
  const a = await open(page, `${tag} a: the disk of db-1 fills`)
  const b = await open(page, `${tag} b: the disk of db-2 fills`)
  const c = await open(page, `${tag} c: the disk of db-3 fills`)
  // At P1 a fix needs an RCA: Close as fixed without one is refused for this one.
  const urgent = await open(page, `${tag} d: checkout down`, { priority: 1 })

  await page.goto(`/#/aiops/inbox?q=${encodeURIComponent(tag)}`)
  await expect.poll(() => listed(page)).toHaveLength(4)
  await expect(page.getByTestId('inbox-bulkbar')).toHaveCount(0)
  // The heading's box picks every case in view.
  await page.getByTestId('inbox-pick-all').check()
  const bar = page.getByTestId('inbox-bulkbar')
  await expect(bar.getByTestId('inbox-bulk-count')).toHaveText('4 selected')

  // Close… with Fixed: one text for all, no RCA.
  await bar.getByTestId('inbox-bulk-close').click()
  const dialog = page.getByTestId('confirm')
  await dialog.getByTestId('inbox-bulk-close-fix').check()
  await dialog.getByTestId('inbox-bulk-text').fill('Cleaned /var on every db host.')
  // The RCA's fields always show, required for the P1 case (CF-42); without a cause that one is refused.
  await expect(dialog.getByTestId('inbox-bulk-rca-add')).toHaveCount(0)
  await expect(dialog.getByTestId('inbox-bulk-rca-hint')).toContainText('Required for the P0 and P1 cases.')
  await expect(dialog.getByTestId('inbox-bulk-rca-fix')).toHaveCount(0)
  await expect(dialog.getByTestId('inbox-bulk-rca-refused')).toContainText('the P0 and P1 cases are refused')
  await dialog.getByTestId('confirm-ok').click()

  // The results, case by case.
  const results = page.getByTestId('inbox-bulk-results')
  await expect(results.getByTestId('inbox-bulk-result')).toHaveCount(4)
  for (const x of [a, b, c]) await expect(results.locator(`[data-id="${x.id}"]`)).toHaveAttribute('data-ok', 'true')
  const refused = results.locator(`[data-id="${urgent.id}"]`)
  await expect(refused).toHaveAttribute('data-ok', 'false')
  await expect(refused).toContainText('refused')
  await expect(page.getByTestId('confirm')).toContainText('3 of 4 done')
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()

  // The server closed the three as fixed; the P1 one is open and still picked.
  for (const x of [a, b, c]) expect(await read(page, x.id)).toMatchObject({ status: 'closed', outcome: 'fix' })
  expect((await read(page, urgent.id)).status).not.toBe('closed')
  await expect(bar.getByTestId('inbox-bulk-count')).toHaveText('1 selected')

  // The refused one closes as no action needed, with one explanation;
  // the old two are gone (CF-90).
  await bar.getByTestId('inbox-bulk-close').click()
  await expect(page.getByTestId('confirm').getByTestId('inbox-bulk-close-noFixNeeded')).toHaveCount(0)
  await expect(page.getByTestId('confirm').getByTestId('inbox-bulk-close-notAProblem')).toHaveCount(0)
  await page.getByTestId('confirm').getByTestId('inbox-bulk-close-noAction').check()
  await page.getByTestId('confirm').getByTestId('inbox-bulk-text').fill('It came back by itself after the failover.')
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  await expect(page.getByTestId('inbox-bulk-results').locator(`[data-id="${urgent.id}"]`)).toHaveAttribute('data-ok', 'true')
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  await expect(page.getByTestId('inbox-bulkbar')).toHaveCount(0)
  expect(await read(page, urgent.id)).toMatchObject({ status: 'closed', outcome: 'noAction' })
  // Pending no longer lists them.
  await expect.poll(() => listed(page)).toHaveLength(0)
})

test('fixed together with one RCA: what was done is its fix, each case keeps it and names the others, and the Inbox finds them by its cause (CF-42, wave 6e)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const one = await open(page, `${tag} rca 1: inotify errors on node-1`)
  const two = await open(page, `${tag} rca 2: inotify errors on node-2`)
  // A word only the RCA's cause has: the search finds the cases by it, not by their titles.
  const word = `watchcap${Date.now()}`

  await page.goto(`/#/aiops/inbox?q=${encodeURIComponent(`${tag} rca`)}`)
  await expect.poll(() => listed(page)).toEqual(expect.arrayContaining([one.id, two.id]))
  for (const x of [one, two]) await page.locator(`[data-testid="inbox-pick"][data-id="${x.id}"]`).check()
  await page.getByTestId('inbox-bulk-close').click()
  const dialog = page.getByTestId('confirm')
  await dialog.getByTestId('inbox-bulk-close-fix').check()
  await dialog.getByTestId('inbox-bulk-text').fill('Raised fs.inotify.max_user_watches on both nodes.')
  await expect(dialog.getByTestId('inbox-bulk-rca-hint')).toContainText('kept on each case')
  await dialog.getByTestId('inbox-bulk-rca-cause').fill(`The default ${word} of inotify watches was too low for the log agents.`)
  await dialog.getByTestId('inbox-bulk-rca-prevention').fill('Set the limit in the node image.')
  await dialog.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('inbox-bulk-results').getByTestId('inbox-bulk-result')).toHaveCount(2)
  await expect(page.getByTestId('inbox-bulk-results').locator('[data-ok="false"]')).toHaveCount(0)
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()

  // Each case keeps the one RCA, its fix what was done, with no id of its own, and names the other.
  for (const [x, other] of [
    [one, two],
    [two, one],
  ] as const) {
    const v = (await read(page, x.id)) as unknown as { rca?: { id?: string; fix: string; cause: string; cases?: string[] } }
    expect(v.rca).toMatchObject({ fix: 'Raised fs.inotify.max_user_watches on both nodes.', cases: [other.id] })
    expect(v.rca?.cause).toContain(word)
    expect(v.rca?.id).toBeUndefined()
  }
  await page.goto(`/#/aiops/inbox/case/${one.number}`)
  const rca = page.getByTestId('case-drawer').getByTestId('case-rca')
  await expect(rca).toContainText(word)
  await expect(rca.getByTestId('case-rca-together')).toContainText(`Closed together with ${two.id} (one RCA)`)
  await expect(rca.getByTestId('case-rca-together-link')).toHaveAttribute('href', `#/aiops/inbox/case/${two.number}`)

  // Past RCAs are found by searching the closed cases: All, and a word of the cause.
  await page.goto(`/#/aiops/inbox?show=all&q=${word}`)
  await expect.poll(() => listed(page)).toEqual(expect.arrayContaining([one.id, two.id]))
})

test('closes many as duplicates of one open case; the original cannot be one of them (CF-38)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const original = await open(page, `${tag} original: VPN drops every hour`)
  const one = await open(page, `${tag} dup 1: VPN drops`)
  const two = await open(page, `${tag} dup 2: the VPN keeps dropping`)

  await page.goto(`/#/aiops/inbox?q=${encodeURIComponent(`${tag} dup`)}`)
  await expect.poll(() => listed(page)).toEqual(expect.arrayContaining([one.id, two.id]))
  for (const x of [one, two]) await page.locator(`[data-testid="inbox-pick"][data-id="${x.id}"]`).check()
  await expect(page.getByTestId('inbox-bulk-count')).toHaveText('2 selected')
  await page.getByTestId('inbox-bulk-close').click()
  const dialog = page.getByTestId('confirm')
  await dialog.getByTestId('inbox-bulk-close-duplicate').check()
  await dialog.getByTestId('inbox-bulk-original').fill(one.id)
  await dialog.getByTestId('inbox-bulk-text').fill('The same VPN problem.')
  await expect(dialog).toContainText('The original cannot be one of the picked cases.')
  await expect(dialog.getByTestId('confirm-ok')).toBeDisabled()
  await dialog.getByTestId('inbox-bulk-original').fill(original.id)
  await dialog.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('inbox-bulk-results').getByTestId('inbox-bulk-result')).toHaveCount(2)
  await expect(page.getByTestId('inbox-bulk-results').locator('[data-ok="false"]')).toHaveCount(0)
  await page.getByTestId('confirm').getByTestId('confirm-ok').click()
  for (const x of [one, two]) expect(await read(page, x.id)).toMatchObject({ status: 'closed', outcome: 'duplicate', duplicateOf: original.id })
  expect((await read(page, original.id)).status).not.toBe('closed')
})

test('the bulk route answers for each case, and refuses an empty list or more than 100 (CF-38)', async ({ page }) => {
  await page.goto('/#/nodes')
  const x = await open(page, `${tag} route: one case`)
  const ok = await page.request.post('/api/v1/aiops/cases/bulk', { headers: SAME, data: { cases: [x.id, 'C-999999'], action: 'priority', priority: 2 } })
  expect(ok.status(), await ok.text()).toBe(200)
  const body = (await ok.json()) as { results: { id: string; ok: boolean; error?: string }[] }
  expect(body.results.find((r) => r.id === x.id)).toMatchObject({ ok: true })
  const missing = body.results.find((r) => r.id === 'C-999999')
  expect(missing?.ok).toBe(false)
  expect(missing?.error).toBeTruthy()
  expect((await read(page, x.id)) as unknown as { priority: number }).toMatchObject({ priority: 2 })
  expect((await page.request.post('/api/v1/aiops/cases/bulk', { headers: SAME, data: { cases: [], action: 'analyze' } })).status()).toBe(400)
  const many = Array.from({ length: 101 }, (_, i) => `C-${i + 1}`)
  expect((await page.request.post('/api/v1/aiops/cases/bulk', { headers: SAME, data: { cases: many, action: 'analyze' } })).status()).toBe(400)
})
