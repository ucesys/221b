// Live updates and recovery, as P1-10, P1-11, P1-12 and P1-16 describe
// them. These tests change the cluster, and put it back.
import { expect, test, type Page } from '@playwright/test'
import { allNodesReady, cluster, consoleNode, healthyWorkers, inKind, podCrashing, snapshot } from './cluster'

async function open(page: Page) {
  await page.goto('/')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

function row(page: Page, node: string) {
  return page.locator(`[data-node="${node}"]`)
}

/** The status column of a row: its second cell. */
function status(page: Page, node: string) {
  return row(page, node).locator(':scope > span').nth(1)
}

test('a node cordoned with kubectl shows as cordoned within 5 seconds', async ({ page, request }) => {
  await open(page)
  const node = healthyWorkers(await snapshot(request))[0]!.name
  await expect(status(page, node)).toHaveText('Ready')
  const started = Date.now()
  await cluster.cordon(node, true)
  try {
    await expect(status(page, node)).toHaveText('Cordoned', { timeout: 5_000 })
    test.info().annotations.push({ type: 'cordon shown after', description: `${Date.now() - started} ms` })
    await expect(row(page, node)).toContainText('1 warning')
  } finally {
    await cluster.cordon(node, false)
  }
  await expect(status(page, node)).toHaveText('Ready', { timeout: 5_000 })
})

test('a node that stops reporting and a crash-looping pod each show as a problem with its cause', async ({ page, request }) => {
  test.setTimeout(inKind ? 360_000 : 60_000)
  // A retry starts only once the last attempt's node is back.
  await allNodesReady()
  await open(page)
  const skip = await consoleNode()
  const workers = healthyWorkers(await snapshot(request)).filter((r) => r.name !== skip)
  const quiet = workers[0]!.name
  // The crash-looping pod goes on another node: the console's own, in kind.
  const crashing = inKind ? skip : workers[1]!.name
  expect(crashing).not.toBe(quiet)

  await cluster.stopKubelet(quiet)
  const crash = await cluster.crashLoop(crashing)
  try {
    // kind marks the node Unknown after the node monitor grace period.
    await expect(status(page, quiet)).toHaveText('Unknown', { timeout: inKind ? 120_000 : 10_000 })
    if (inKind) {
      // Wait for the cluster itself to say the pod is crash-looping, then
      // for the console to show it.
      await expect.poll(() => podCrashing(crash.pod), { timeout: 120_000, intervals: [1000] }).toBe(true)
    }
    await expect(row(page, crashing)).toContainText(/1 error/, { timeout: 10_000 })

    await row(page, quiet).click()
    const panel = page.getByTestId('node-panel')
    await expect(panel.getByRole('region', { name: 'Problems' })).toContainText('Kubelet stopped reporting')
    await expect(panel.getByRole('region', { name: 'Problems' })).toContainText('NodeStatusUnknown')

    await row(page, crashing).click()
    await expect(panel.getByRole('heading', { level: 2 })).toHaveText(crashing)
    const problems = panel.getByRole('region', { name: 'Problems' })
    await expect(problems).toContainText(/pods? not ready/)
    await expect(problems).toContainText('CrashLoopBackOff', { timeout: inKind ? 60_000 : 5_000 })
    await expect(problems).toContainText(crash.pod)
  } finally {
    await cluster.startKubelet(quiet)
    await crash.cleanup()
  }
  await expect(status(page, quiet)).toHaveText('Ready', { timeout: inKind ? 120_000 : 10_000 })
})

test('works without metrics-server and marks usage as unavailable', async ({ page }) => {
  test.setTimeout(inKind ? 300_000 : 60_000)
  await open(page)
  const card = page.getByTestId('card-cpu')
  const used = card.locator('.rk-sw[data-k="use"] + span + span')
  await expect(used).not.toHaveText('—', { timeout: inKind ? 120_000 : 20_000 })

  await cluster.metricsServer(false)
  try {
    await expect(card).toContainText('metrics-server is not installed', { timeout: 40_000 })
    await expect(used).toHaveText('—')
    await expect(page.locator('[data-node]').first()).toBeVisible()
  } finally {
    await cluster.metricsServer(true)
  }
  await expect(used).not.toHaveText('—', { timeout: inKind ? 180_000 : 20_000 })
})

test('turns red without the API server and recovers by itself', async ({ page, request }) => {
  test.setTimeout(inKind ? 360_000 : 90_000)
  await open(page)
  const node = healthyWorkers(await snapshot(request))[0]!.name
  const state = page.getByTestId('cluster-state')

  const lost = Date.now()
  await cluster.apiServer(false)
  try {
    await expect(state).toHaveText('Disconnected', { timeout: 30_000 })
    test.info().annotations.push({ type: 'red after', description: `${Date.now() - lost} ms` })
    await expect(page.getByText('Cannot reach the Kubernetes API server', { exact: true })).toBeVisible()
    // The last known state stays on screen.
    await expect(row(page, node)).toBeVisible()
  } finally {
    await cluster.apiServer(true)
  }
  const back = Date.now()
  await expect(state).toHaveText('Connected', { timeout: 30_000 })
  test.info().annotations.push({ type: 'green after', description: `${Date.now() - back} ms` })

  // Current again without a reload: a change made now shows up.
  await cluster.cordon(node, true)
  try {
    await expect(status(page, node)).toHaveText('Cordoned', { timeout: 30_000 })
  } finally {
    await cluster.cordon(node, false)
  }
  await expect(status(page, node)).toHaveText('Ready', { timeout: 30_000 })
})
