// Saved fixes (Phase 12, wave 6g): a case closed as Fixed with Save fix
// on (the default) becomes a fix in AIOps › Mycroft › Fixes, which the
// case links ("Saved as F-n"); the fix is active, lists the case under
// Seen in, and is edited there. The Fixes, Known issues and Exceptions
// tables sort by their Updated column, newest first, either way.
import type { Page } from '@playwright/test'
import { expect, test } from './test'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }

async function call(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === null ? undefined : JSON.stringify(b) })).status, [path, body, method] as const)
}

/** Opens a case by hand through the API. */
async function open(page: Page, title: string): Promise<{ id: string; number: number }> {
  const res = await page.request.post('/api/v1/aiops/cases', { headers: SAME, data: { title, priority: 3 } })
  expect(res.status(), await res.text()).toBe(201)
  return (await res.json()) as { id: string; number: number }
}

let fixId = ''

test.afterAll(async ({ browser }) => {
  // The fix deleted and any case left open closed, so the other specs see the console as before.
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  if (fixId) await call(page, `/api/v1/aiops/fixes/${fixId}`, null, 'DELETE')
  const list = (await (await page.request.get('/api/v1/aiops/cases?open=1')).json()) as { cases: { id: string; title: string }[] }
  for (const c of list.cases.filter((x) => x.title.startsWith('Fixes e2e'))) await call(page, `/api/v1/aiops/cases/${c.id}/close`, { outcome: 'noAction', explanation: 'e2e: done' }, 'POST')
  await page.close()
})

test('closing a case as Fixed with Save fix on saves a fix: the case links it, Mycroft › Fixes lists it active with the case, and it is edited there (wave 6g)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await call(page, '/api/v1/jobs/case-analysis', { on: false })).toBe(200)
  const stamp = Date.now()
  const c = await open(page, `Fixes e2e: the cache of web-1 fills its disk ${stamp}`)

  await page.goto(`/#/aiops/inbox/case/${c.number}`)
  const drawer = page.getByTestId('case-drawer')
  await expect(drawer).toBeVisible()
  await drawer.getByTestId('case-close-open').click()
  await drawer.getByTestId('case-outcome-fix').check()
  await drawer.getByTestId('case-fix-text').fill(`Cleared the cache of web-1 and gave it a size limit (${stamp}).`)
  await drawer.getByTestId('case-rca-cause').fill('The cache had no size limit.')
  // Save fix is on unless someone turns it off.
  await expect(drawer.getByTestId('case-save-fix')).toBeChecked()
  await expect(drawer.getByTestId('case-fix-form')).toContainText('Mycroft › Fixes')
  await drawer.getByTestId('case-fix-save').click()
  await expect(drawer.getByTestId('case-status')).toHaveText('Closed: fixed')
  const saved = drawer.getByTestId('case-saved-fix')
  await expect(saved).toHaveText(/^F-\d+$/)
  await expect(saved).toHaveAttribute('href', /^#\/aiops\/mycroft\/fixes\/F-\d+$/)
  await expect(drawer.getByTestId('case-closed')).toContainText(/Saved as F-\d+/)
  fixId = ((await saved.textContent()) ?? '').trim()
  const kase = (await (await page.request.get(`/api/v1/aiops/cases/${c.id}`)).json()) as { fix?: string }
  expect(kase.fix).toBe(fixId)

  // Mycroft › Fixes: active, newest first, with the case under Seen in.
  await saved.click()
  await expect(page).toHaveURL(new RegExp(`#/aiops/mycroft/fixes/${fixId}$`))
  await expect(page.getByTestId('mycroft-sub-fixes')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('fixes-show-active')).toHaveAttribute('aria-pressed', 'true')
  const row = page.locator(`[data-testid="fix-row"][data-id="${fixId}"]`)
  await expect(row.getByTestId('fix-status')).toHaveText('active')
  await expect(row.getByTestId('fix-cases')).toHaveText('1')
  const fd = page.getByTestId('fix-drawer')
  await expect(fd).toBeVisible()
  const seen = fd.getByTestId('fix-seen')
  await expect(seen).toContainText(c.id)
  await expect(seen.getByRole('link', { name: c.id })).toHaveAttribute('href', `#/aiops/inbox/case/${c.number}`)

  // Edit its steps and save: the drawer and the API say the new steps.
  await fd.getByTestId('fix-edit').click()
  await expect(fd.getByTestId('fix-save')).toBeDisabled()
  const steps = `Clear the cache, then set a size limit on it (${stamp}).`
  await fd.getByTestId('fix-steps').fill(steps)
  await fd.getByTestId('fix-save').click()
  await expect(fd.getByTestId('fix-editor')).toHaveCount(0)
  await expect(fd.getByTestId('fix-steps-text')).toHaveText(steps)
  const got = (await (await page.request.get(`/api/v1/aiops/fixes/${fixId}`)).json()) as { steps?: string; status?: string; cases?: { case: string }[] }
  expect(got).toMatchObject({ steps, status: 'active' })
  expect((got.cases ?? []).map((x) => x.case)).toContain(c.id)

  // The table sorts by Updated: newest first, so the fix just edited leads; a click turns it round.
  await fd.getByRole('button', { name: 'Close fix' }).last().click()
  await expect(page).toHaveURL(/#\/aiops\/mycroft\/fixes$/)
  const updated = page.getByTestId('fixes-table').locator('th:has(button[data-col="updated"])')
  await expect(updated).toHaveAttribute('aria-sort', 'descending')
  await expect(page.getByTestId('fix-row').first()).toHaveAttribute('data-id', fixId)
  await updated.locator('button').click()
  await expect(updated).toHaveAttribute('aria-sort', 'ascending')
  await updated.locator('button').click()
  await expect(updated).toHaveAttribute('aria-sort', 'descending')
  await expect(page.getByTestId('fix-row').first()).toHaveAttribute('data-id', fixId)
})

test('Known issues and Exceptions sort by their Updated column, either way (wave 6g)', async ({ page }) => {
  await page.goto('/#/aiops/mycroft/known')
  const known = page.getByTestId('known-table')
  await expect(known).toBeVisible({ timeout: 20_000 })
  const ku = known.locator('th:has(button[data-col="updated"])')
  await expect(ku).toHaveAttribute('aria-sort', 'descending')
  await ku.locator('button').click()
  await expect(ku).toHaveAttribute('aria-sort', 'ascending')
  await ku.locator('button').click()
  await expect(ku).toHaveAttribute('aria-sort', 'descending')
  await expect(page.getByTestId('known-show-inactive')).toBeVisible()

  // Exceptions: the table shows once there is one, removed ones under All.
  await page.getByTestId('mycroft-sub-exceptions').click()
  await expect(page.getByTestId('knowledge-exceptions')).toBeVisible()
  await page.getByTestId('exceptions-show-all').click()
  const all = (await (await page.request.get('/api/v1/aiops/exceptions')).json()) as { exceptions: unknown[] }
  if (all.exceptions.length === 0) {
    await expect(page.getByTestId('exceptions-empty')).toBeVisible()
    return
  }
  const ex = page.getByTestId('exceptions-table')
  await expect(ex).toBeVisible()
  const eu = ex.locator('th:has(button[data-col="updated"])')
  await expect(eu).toHaveAttribute('aria-sort', 'descending')
  await eu.locator('button').click()
  await expect(eu).toHaveAttribute('aria-sort', 'ascending')
  await eu.locator('button').click()
  await expect(eu).toHaveAttribute('aria-sort', 'descending')
})
