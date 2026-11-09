// Cases › Mycroft's sub-tabs share one column and one page layout
// (CF-55): at 1440px the Known issues, Exceptions, Fixes and Validation
// rules tables are as wide as each other and start at the same left edge,
// whatever they hold (an empty one keeps its header), and the Wiki sits
// in the same column. Each page's intro line spans the column, as on Jobs. At a phone's width the toolbar wraps inside the
// screen.
import { expect, test } from './test'

const TABLES = [
  ['known', 'known-table'],
  ['exceptions', 'exceptions-table'],
  ['fixes', 'fixes-table'],
  ['rules', 'rules-table'],
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
  await page.goto('/#/cases/mycroft/rules')

  // The Wiki's page fills the same column as the others' pages.
  const rules = (await page.getByTestId('mycroft-rules').boundingBox())!
  await page.getByTestId('mycroft-sub-wiki').click()
  await expect(page.getByTestId('knowledge-tab')).toBeVisible()
  const wiki = (await page.getByTestId('knowledge-tab').boundingBox())!
  expect(Math.abs(wiki.x - rules.x)).toBeLessThanOrEqual(2)
  expect(Math.abs(wiki.width - rules.width)).toBeLessThanOrEqual(2)

  // On a phone the toolbar wraps inside the page: the filter, the job
  // select, the search and Add rule all within the screen's width; the
  // table scrolls in its card.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByTestId('mycroft-sub-rules').click()
  await expect(page.getByTestId('rules-table')).toBeVisible()
  const bar = page.getByTestId('mycroft-toolbar')
  const barBox = (await bar.boundingBox())!
  expect(barBox.x + barBox.width).toBeLessThanOrEqual(390)
  for (const id of ['rules-show', 'rules-job', 'rules-search']) {
    const b = (await bar.getByTestId(id).boundingBox())!
    expect(b.x + b.width, `${id} fits`).toBeLessThanOrEqual(390)
  }
})
