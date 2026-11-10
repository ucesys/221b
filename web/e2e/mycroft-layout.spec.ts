// AIOps › Mycroft's sub-tabs share one column and one page layout
// (CF-55): at 1440px the Known issues, Exceptions and Fixes tables are as
// wide as each other and start at the same left edge, whatever they hold
// (an empty one keeps its header), and the Wiki sits in the same column.
// Each page's intro line spans the column, as on Jobs. Validation rules,
// in Jobs since CF-89, keep the same page layout and table in the Jobs
// column, as wide as Mycroft's. At a phone's width their toolbar wraps
// inside the screen.
import { expect, test } from './test'

const TABLES = [
  ['known', 'known-table'],
  ['exceptions', 'exceptions-table'],
  ['fixes', 'fixes-table'],
] as const

test('the Mycroft tables are one width and start at one left edge at 1440px, and the toolbar wraps on a phone (CF-55)', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/#/cases/mycroft/known')
  await expect(page.getByTestId('mycroft-tab')).toBeVisible({ timeout: 20_000 })
  const boxes: { sub: string; x: number; width: number }[] = []
  for (const [sub, id] of TABLES) {
    await page.getByTestId(`mycroft-sub-${sub}`).click()
    await expect(page).toHaveURL(new RegExp(`#/cases/mycroft/${sub}$`))
    const table = page.getByTestId(id)
    await expect(table).toBeVisible({ timeout: 20_000 })
    await expect(table.locator('thead th').first()).toBeVisible()
    const box = (await table.boundingBox())!
    boxes.push({ sub, x: box.x, width: box.width })
    // The intro line is as wide as the toolbar under it: the whole column.
    const lead = (await page.locator('.cs-lead').boundingBox())!
    const bar = (await page.getByTestId('mycroft-toolbar').boundingBox())!
    expect(Math.abs(lead.width - bar.width), `${sub}'s intro spans the column`).toBeLessThanOrEqual(2)
  }
  const first = boxes[0]!
  for (const b of boxes.slice(1)) {
    expect(Math.abs(b.width - first.width), `${b.sub} is as wide as ${first.sub}`).toBeLessThanOrEqual(2)
    expect(Math.abs(b.x - first.x), `${b.sub} starts where ${first.sub} does`).toBeLessThanOrEqual(2)
  }
  // The column is the Inbox's: wide, its side margins about a quarter of
  // the 1280px column's (CF-66), as wide as the Inbox's own.
  const mycroft = (await page.getByTestId('mycroft-tab').boundingBox())!
  await page.goto('/#/cases/inbox')
  const inbox = (await page.getByTestId('cases-inbox').boundingBox())!
  expect(Math.abs(mycroft.width - inbox.width)).toBeLessThanOrEqual(2)
  expect(first.width).toBeGreaterThan(1000)
  // The Wiki's page fills the same column as the others' pages.
  await page.goto('/#/cases/mycroft/known')
  const known = (await page.getByTestId('cases-known').boundingBox())!
  await page.getByTestId('mycroft-sub-wiki').click()
  await expect(page.getByTestId('knowledge-tab')).toBeVisible()
  const wiki = (await page.getByTestId('knowledge-tab').boundingBox())!
  expect(Math.abs(wiki.x - known.x)).toBeLessThanOrEqual(2)
  expect(Math.abs(wiki.width - known.width)).toBeLessThanOrEqual(2)
  // No Validation rules here: they are in Jobs (CF-89), where their
  // table is as wide as Mycroft's and starts at the same left edge.
  await expect(page.getByTestId('mycroft-sub-rules')).toHaveCount(0)
  await page.goto('/#/cases/jobs/rules')
  const rulesTable = page.getByTestId('rules-table')
  await expect(rulesTable).toBeVisible({ timeout: 20_000 })
  const rt = (await rulesTable.boundingBox())!
  expect(Math.abs(rt.width - first.width), 'rules are as wide as known').toBeLessThanOrEqual(2)
  expect(Math.abs(rt.x - first.x), 'rules start where known does').toBeLessThanOrEqual(2)

  // On a phone the toolbar wraps inside the page: the filter, the job
  // select, the search and Add rule all within the screen's width; the
  // table scrolls in its card.
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByTestId('rules-table')).toBeVisible()
  const bar = page.getByTestId('mycroft-toolbar')
  const barBox = (await bar.boundingBox())!
  expect(barBox.x + barBox.width).toBeLessThanOrEqual(390)
  for (const id of ['rules-show', 'rules-job', 'rules-search']) {
    const b = (await bar.getByTestId(id).boundingBox())!
    expect(b.x + b.width, `${id} fits`).toBeLessThanOrEqual(390)
  }
})
