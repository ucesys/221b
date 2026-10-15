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
  // Recovered pods too: the out-of-memory pod may be running again.
  await open(page, '#/alerts?show=all')
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
    await expect(page).toHaveURL(/#\/alerts\?.*peek=pod/)
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
    // Opening a node shows its details over the Alerts tab.
    await c.getByTestId('alert-open').click()
    await expect(page.getByTestId('node-panel')).toContainText(node)
    await expect(page).toHaveURL(new RegExp(`#/alerts\\?.*peek=node%3A${node}`))
    await page.keyboard.press('Escape')
  } finally {
    await cluster.cordon(node, false)
  }
  await open(page)
  await expect(card(page, `node:${node}`)).toHaveCount(0, { timeout: 15_000 })
})

test('an acknowledged alert leaves the banners and the badge for everyone, and stays here', async ({ page, request }) => {
  const node = await worker(request)
  await open(page)
  const f = await cluster.alertFaults(node)
  try {
    const pull = card(page, `pod:${f.pull}`)
    await expect(pull).toHaveAttribute('data-severity', 'failed', { timeout: 30_000 })
    const badge = async () => Number(((await page.locator('[data-tab="alerts"]').first().textContent()) ?? '').replace(/\D+/g, '') || 0)
    const before = await badge()

    await pull.getByTestId('alert-ack').click()
    await expect(pull.getByTestId('alert-acked')).toContainText('Acknowledged by')
    await expect(pull).toHaveAttribute('data-acked', 'true')
    // Yellow, not red (asked at the Phase 4 review).
    await expect(pull.getByTestId('alert-sev')).toHaveAttribute('data-tone', 'warn')
    await expect(pull.getByTestId('alert-acked')).toHaveAttribute('data-tone', 'warn')
    await expect.poll(badge).toBe(before - 1)
    // The tab's name keeps its colour while any alert is on, acknowledged or not.
    await expect(page.locator('[data-tab="alerts"]').first()).toHaveAttribute('data-tone', /^(err|warn)$/)
    // Everyone sees it: the server marks the row.
    await expect
      .poll(async () => ((await snapshot(request, ['alerts'])).views.alerts?.rows as { key: string; ack?: { by: string } }[]).find((r) => r.key === `pod:${f.pull}`)?.ack?.by ?? '')
      .not.toBe('')
    expect((await cluster.auditLog()).some((r) => r.action === 'alert.ack' && r.target === `pod:${f.pull}`)).toBe(true)

    // Not on the other tabs' banners; the pending pod still is.
    await open(page, '#/nodes')
    await expect(page.getByTestId('alert-banner').filter({ hasText: /pending/ })).toContainText(f.unschedulable)
    await expect(page.getByTestId('alert-banner').filter({ hasText: f.pull })).toHaveCount(0)

    // Taken back, it is on them again.
    await open(page)
    await pull.getByTestId('alert-ack').click()
    await expect(pull).not.toHaveAttribute('data-acked', 'true')
    await expect(pull.getByTestId('alert-sev')).toHaveAttribute('data-tone', 'err')
    await expect(page.locator('[data-tab="alerts"]').first()).toHaveAttribute('data-tone', 'err')
    await open(page, '#/nodes')
    await expect(page.getByTestId('alert-banner').filter({ hasText: /failed to start/ })).toContainText(f.pull)
  } finally {
    await f.cleanup()
  }
})

test('only active alerts by default; recovered pods go last, and the empty groups after them', async ({ page, request }) => {
  await open(page)
  const active = page.getByTestId('alert-active')
  await expect(active).toBeChecked()
  const rows = (await snapshot(request, ['alerts'])).views.alerts?.rows as { key: string; severity: string }[]
  const recovered = rows.filter((r) => r.severity === 'recovered')
  for (const r of recovered) await expect(card(page, r.key)).toHaveCount(0)
  await active.uncheck()
  await expect(page).toHaveURL(/show=all/)
  if (recovered.length) {
    const tag = card(page, recovered[0]!.key).locator('.rk-tag').first()
    await expect(tag).toHaveText('Recovered')
    await expect(tag).toHaveAttribute('data-tone', 'ok')
  }
  // The groups in order: what is going on, then recovered pods, then the empty ones.
  const ids = await page.locator('[data-testid^="alert-section-"]').evaluateAll((els) => els.map((e) => ({ id: e.getAttribute('data-testid')!.slice('alert-section-'.length), empty: !!e.querySelector('[data-testid="alert-empty"]') })))
  const firstEmpty = ids.findIndex((x) => x.empty)
  if (firstEmpty >= 0) expect(ids.slice(firstEmpty).every((x) => x.empty)).toBe(true)
  const rec = ids.findIndex((x) => x.id === 'recovered')
  if (rec >= 0) expect(ids.slice(0, rec).every((x) => !x.empty)).toBe(true)
  await active.check()
  await expect(page).not.toHaveURL(/show=all/)
})
