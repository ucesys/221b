// Navigation as designed (P2-01): Nodes, Namespaces, Pods and Helm in the
// bar, Services under More, and the More menu takes tabs as the window
// narrows; the open tab always shows.
import { expect, test } from '@playwright/test'

const TABS = ['Nodes', 'Namespaces', 'Pods', 'Helm']

test('the bar shows the four tabs and Services under More', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const nav = page.getByRole('navigation', { name: 'Views' })
  for (const t of TABS) await expect(nav.getByRole('link', { name: new RegExp(`^${t}`) })).toBeVisible()
  await expect(nav.getByRole('link', { name: /^Services/ })).toHaveCount(0)
  await page.getByTestId('more').click()
  const menu = page.getByRole('menu', { name: 'More views' })
  await expect(menu.getByRole('menuitem', { name: /^Services/ })).toBeVisible()
  await menu.getByRole('menuitem', { name: /^Services/ }).click()
  await expect(page).toHaveURL(/#\/services/)
  // The open tab takes a place of its own when there is room.
  await expect(nav.getByRole('link', { name: /^Services/ })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByTestId('more')).toHaveCount(0)
  await expect(page).toHaveTitle(/Services · 221B/)
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
