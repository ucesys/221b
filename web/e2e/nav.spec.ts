// Navigation as designed (P2-01): every tab in the bar while they fit, and
// the More menu takes tabs from the right as the window narrows; the open
// tab always shows.
import { expect, test } from './test'

const TABS = ['Nodes', 'Namespaces', 'Pods', 'Helm', 'Services']

test('the bar shows every tab while they fit, and More takes them as the window narrows', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const nav = page.getByRole('navigation', { name: 'Views' })
  for (const t of TABS) await expect(nav.getByRole('link', { name: new RegExp(`^${t}`) })).toBeVisible()
  // Nothing is rolled up while there is room (asked at the Phase 2 review).
  await expect(page.getByTestId('more')).toHaveCount(0)

  // Narrow: the last tabs move under More, in their order.
  await page.setViewportSize({ width: 420, height: 844 })
  await expect(page.getByTestId('more')).toBeVisible()
  await expect(nav.getByRole('link', { name: /^Services/ })).toHaveCount(0)
  await page.getByTestId('more').click()
  const menu = page.getByRole('menu', { name: 'More views' })
  await expect(menu.getByRole('menuitem').last()).toHaveText(/^Services/)
  await menu.getByRole('menuitem', { name: /^Services/ }).click()
  await expect(page).toHaveURL(/#\/services/)
  // The open tab rides on the More button when it does not fit.
  const more = page.getByTestId('more')
  await expect(more).toContainText('Services')
  await expect(more).toHaveAttribute('aria-current', 'page')
  await expect(page).toHaveTitle(/Services · 221B/)

  // Wide again: Services takes its place in the bar and More goes.
  await page.setViewportSize({ width: 1280, height: 844 })
  await expect(nav.getByRole('link', { name: /^Services/ })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('more')).toHaveCount(0)
})

for (const width of [390, 700, 1024]) {
  test(`at ${width} px no tab is cut off and the open tab always shows`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 })
    for (const hash of ['#/nodes', '#/pods', '#/services', '#/helm', '#/namespaces']) {
      await page.goto('/' + hash)
      await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
      const nav = page.getByRole('navigation', { name: 'Views' })
      const navBox = (await nav.boundingBox())!
      // Every tab link in the bar lies within the bar.
      for (const link of await nav.getByRole('link').all()) {
        const box = (await link.boundingBox())!
        expect(box.x + box.width, `${hash} at ${width}: a tab is cut off`).toBeLessThanOrEqual(navBox.x + navBox.width + 1)
      }
      // The open tab is in the bar, or the More button reads as it.
      const label = hash.slice(2, 3).toUpperCase() + hash.slice(3)
      const inBar = nav.getByRole('link', { name: new RegExp(`^${label}`) })
      const more = page.getByTestId('more')
      if ((await inBar.count()) > 0) await expect(inBar).toHaveAttribute('aria-current', 'page')
      else {
        await expect(more).toContainText(label)
        await expect(more).toHaveAttribute('aria-current', 'page')
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
    }
  })
}
