// The home page (asked at the Phase 4 review): the landing page, every
// cluster at a glance, reached from the logo; Alerts is the first tab.
import { expect, test } from './test'
import { nodeRows, snapshot } from './cluster'

test('the console opens on the home page, with each cluster’s health and use', async ({ page, request }) => {
  await page.goto('/')
  await expect(page.getByTestId('home')).toBeVisible()
  await expect(page).toHaveTitle(/^Clusters · 221B/)
  const card = page.locator('[data-testid=home-card][data-cluster=local]')
  await expect(card).toBeVisible()
  await expect(card.getByTestId('home-health')).toHaveText(/Healthy|Warnings|Needs attention/)

  // The figures are the cluster's, as the Nodes tab has them.
  const nodes = nodeRows(await snapshot(request))
  // A cordoned node is ready too.
  const ready = nodes.filter((n) => n.status === 'Ready' || n.status === 'Cordoned').length
  await expect(card.getByTestId('home-nodes')).toContainText(`${ready}/${nodes.length}`)
  // Only the figures: pods ready of those not finished, and claims bound (asked at the second Toolbox review).
  await expect(card.getByTestId('home-pods')).toHaveText(/^\d+\/\d+Pods ready$/)
  await expect(card.getByTestId('home-alerts')).toHaveText(/^\d+Alerts?$/)
  await expect(card.getByTestId('home-storage')).toHaveText(/^\d+\/\d+Claims bound$/)
  await expect(card.getByTestId('home-storage')).toHaveAttribute('href', /#\/c\/local\/storage$|#\/storage$/)
  await expect(card.getByTestId('home-cpu-text')).toContainText(/ of [\d.]+ cores requested \(/)
  await expect(card.getByTestId('home-memory-text')).toContainText(/requested \(/)
  await expect(card.getByTestId('home-podroom-text')).toContainText(/^\d+ of \d+/)

  // Its alerts' figure leads to its Alerts tab, the first tab.
  const tabs = page.getByRole('navigation', { name: 'Views' }).getByRole('link')
  await expect(tabs.first()).toHaveText(/^Alerts/)
  await card.getByTestId('home-alerts').click()
  await expect(page).toHaveURL(/#\/alerts$/)
  await expect(page.locator('[data-tab=alerts]')).toHaveAttribute('aria-current', 'page')

  // The logo leads back home from any tab.
  await page.goto('/#/pods')
  await expect(page.getByTestId('home')).toHaveCount(0)
  await page.getByTestId('home-link').click()
  await expect(page).toHaveURL(/#\/$/)
  await expect(page.getByTestId('home')).toBeVisible()
  await expect(page.getByTestId('home-link')).toHaveAttribute('aria-current', 'page')
})

test('a link with no tab opens the cluster on its first tab', async ({ page }) => {
  await page.goto('/#/c/local')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.locator('[data-tab=alerts]')).toHaveAttribute('aria-current', 'page')
})

test('the home page needs a session, like every other page', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const res = await ctx.request.get('/api/v1/home')
  expect([401, 403]).toContain(res.status())
  await ctx.close()
})
