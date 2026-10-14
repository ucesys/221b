// The Alerts tab and the banners it puts on every other tab (P4-07).
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, consoleNode, healthyWorkers, snapshot } from './cluster'

async function open(page: Page, hash = '#/alerts') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

const card = (page: Page, key: string) => page.locator(`[data-alert="${key}"]`)

/** A worker the tests may break: healthy, and not the one 221B runs on. */
async function worker(request: Parameters<typeof snapshot>[0]): Promise<string> {
  const skip = await consoleNode()
  const w = healthyWorkers(await snapshot(request)).find((r) => r.name !== skip)
  if (!w) throw new Error('no healthy worker to use')
  return w.name
}

test('each injected fault appears within 30 seconds', async ({ page, request }) => {
  const node = await worker(request)
  await open(page)
  const f = await cluster.alertFaults(node)
  try {
    const pull = card(page, `pod:${f.pull}`)
    const nowhere = card(page, `pod:${f.unschedulable}`)
    const oom = card(page, `pod:${f.oom}`)

    // The image that cannot be pulled: the pod failed to start, and says why.
    await expect(pull).toHaveAttribute('data-severity', 'failed', { timeout: 30_000 })
    await expect(pull.getByTestId('alert-context')).toContainText(/ErrImagePull|ImagePullBackOff/)
    await expect(pull).toContainText(node)
    // The pod no node can take: pending, with the scheduler's reason.
    await expect(nowhere).toHaveAttribute('data-severity', 'pending', { timeout: 30_000 })
    await expect(nowhere.getByTestId('alert-context')).toContainText(/didn't match|node affinity|selector/)
    await expect(nowhere.locator('[data-testid="alert-event"][data-reason="FailedScheduling"]')).toHaveCount(1, { timeout: 30_000 })
    // The pod the kernel killed: failing again or running again, but the
    // out-of-memory kill is named either way.
    await expect(oom).toContainText('OOMKilled', { timeout: 30_000 })

    // Grouped by object, with counts and first and last seen.
    const ev = nowhere.locator('[data-testid="alert-event"]').first()
    await expect(ev).toContainText(/×\d+/)
    await expect(ev).toContainText(/ago/)

    // The tiles and the tab's badge count them.
    await expect(page.getByTestId('alert-tiles')).toContainText(/failed to start/i)
    await expect(page.getByRole('link', { name: /^Alerts/ })).not.toHaveText(/^Alerts$/)

    // Searching narrows the cards to the one asked for.
    await page.getByTestId('alert-search').fill(f.pull.split('/')[1]!)
    await expect(pull).toBeVisible()
    await expect(nowhere).toHaveCount(0)
    await page.getByTestId('alert-search').fill('')
    // Nodes only: the pods go.
    await page.getByRole('group', { name: 'Kind of object' }).locator('[data-kind="nodes"]').click()
    await expect(pull).toHaveCount(0)
    await page.getByRole('group', { name: 'Kind of object' }).locator('[data-kind="all"]').click()
    await expect(pull).toBeVisible()

    // Opening the pod shows its details over the tab.
    await pull.getByTestId('alert-open').click()
    await expect(page).toHaveURL(/open=pod/)
    await expect(page.getByTestId('pod-panel')).toContainText(f.pull.split('/')[1]!)
    await page.keyboard.press('Escape')

    // Every other tab gets a banner that leads here.
    await open(page, '#/nodes')
    const banner = page.getByTestId('alert-banner').filter({ hasText: /failed to start/ })
    await expect(banner).toContainText(f.pull)
    await expect(page.getByTestId('alert-banner').filter({ hasText: /pending/ })).toContainText(f.unschedulable)
    await banner.getByTestId('alert-banner-link').click()
    await expect(page).toHaveURL(/#\/alerts/)
    await expect(pull).toBeVisible()
  } finally {
    await f.cleanup()
  }
  // Gone pods have no alerts.
  await expect(card(page, `pod:${f.pull}`)).toHaveCount(0, { timeout: 30_000 })
  await expect(card(page, `pod:${f.unschedulable}`)).toHaveCount(0)
})

test('a cordoned node is a node warning until it is uncordoned', async ({ page, request }) => {
  const node = await worker(request)
  await open(page)
  await cluster.cordon(node, true)
  try {
    const c = card(page, `node:${node}`)
    await expect(c).toHaveAttribute('data-severity', 'warning', { timeout: 15_000 })
    await expect(c.getByTestId('alert-context')).toContainText('Cordoned')
    await expect(c.locator('[data-testid="alert-event"][data-reason="Unschedulable"]')).toContainText('Node spec')
    // Opening a node goes to it on the Nodes tab.
    await c.getByTestId('alert-open').click()
    await expect(page).toHaveURL(new RegExp(`#/nodes.*open=${node}`))
  } finally {
    await cluster.cordon(node, false)
  }
  await open(page)
  await expect(card(page, `node:${node}`)).toHaveCount(0, { timeout: 15_000 })
})
