// The Namespaces tab, as P2-02 and P2-08 describe it.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import type { NamespaceRow, ObjectRow, ReleaseRow } from '../src/api/types'
import { cluster, countKind, inKind, PLANTED, snapshot } from './cluster'

async function open(page: Page, hash = '#/namespaces') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.getByTestId('namespace-name')).toBeVisible()
}

test('lists every namespace and what it holds, by kind, with counts that match the cluster', async ({ page, request }) => {
  await open(page)
  const namespaces = (await snapshot(request, ['namespaces'])).views.namespaces!.rows as NamespaceRow[]
  expect(namespaces.length).toBeGreaterThan(0)
  const list = page.getByTestId('namespace-list')
  for (const ns of namespaces) await expect(list.getByRole('link', { name: ns.name, exact: true })).toBeVisible()

  // The namespace with the most pods, opened from the list.
  const ns = [...namespaces].sort((a, b) => b.pods - a.pods)[0]!
  await list.getByRole('link', { name: ns.name, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`#/namespaces/${ns.name}`))
  await expect(page.getByTestId('namespace-name')).toHaveText(ns.name)
  await expect(page.getByTestId('namespace-summary')).toContainText(`${ns.objects} objects of ${ns.kinds} kinds`)

  // Each kind's tab says how many there are, and that matches the cluster.
  const objects = (await snapshot(request, [`ns:${ns.name}`])).views[`ns:${ns.name}`]!.rows as ObjectRow[]
  const tabs = page.getByTestId('namespace-kinds')
  for (const kind of ['pods', 'services', 'configmaps', 'deployments']) {
    const want = await countKind(ns.name, kind)
    const rows = objects.filter((o) => o.kindName === kind)
    expect(rows.length, `${kind} in ${ns.name}`).toBe(want)
    expect(ns.counts[kind] ?? 0, `${kind} count of ${ns.name}`).toBe(want)
    if (want > 0) {
      const tab = tabs.locator(`[data-subtab="${rows[0]!.kind}"]`)
      await expect(tab.locator('.rk-count')).toHaveText(String(want))
      await tab.click()
      await expect(tab).toHaveAttribute('aria-current', 'page')
      // The table holds them all; long tables render only the rows in view.
      await expect(page.locator(`section[data-kind="${rows[0]!.kind}"] .rk-grid[data-total]`)).toHaveAttribute('data-total', String(want))
    }
  }
})

test('a tab per kind, as on the Helm tab, Pods first; the search filters by name or detail', async ({ page, request }) => {
  await open(page)
  const ns = await page.getByTestId('namespace-name').textContent()
  const objects = (await snapshot(request, [`ns:${ns}`])).views[`ns:${ns}`]!.rows as ObjectRow[]
  const kinds = [...new Set(objects.map((o) => o.kind))]
  expect(kinds.length).toBeGreaterThan(1)
  const tabs = page.getByTestId('namespace-kinds').locator('[data-subtab]')
  expect(await tabs.count()).toBeGreaterThanOrEqual(kinds.length)
  // One table at a time: the first tab's.
  await expect(page.locator('section[data-kind]')).toHaveCount(1)
  if (kinds.includes('Pod')) await expect(tabs.first()).toHaveAttribute('data-subtab', 'Pod')
  const other = kinds.find((k) => k !== 'Pod')!
  await page.getByTestId('namespace-kinds').locator(`[data-subtab="${other}"]`).click()
  await expect(page.locator('section[data-kind]')).toHaveAttribute('data-kind', other)
  await expect(page).toHaveURL(/kinds=/)
  await tabs.first().click()
  await expect(page).not.toHaveURL(/kinds=/)

  const target = objects.find((o) => o.kind === 'Pod') ?? objects[0]!
  await page.getByTestId('object-search').fill(target.name)
  await expect(page.locator(`[data-object="${target.key}"]`)).toBeVisible()
  await expect(page.locator('[data-object]').first()).toBeVisible()
  const shown = await page.locator('[data-object]').evaluateAll((els) => els.map((e) => e.getAttribute('data-object')))
  for (const k of shown) expect(objects.find((o) => o.key === k)!.name.includes(target.name) || objects.find((o) => o.key === k)!.details.includes(target.name)).toBe(true)
})

test('kinds are labelled with their real plurals: Endpoints, NetworkPolicies, Ingresses', async ({ page, request }) => {
  // Asked at the Phase 2 review: the tab said "Endpointss" and "NetworkPolicys".
  const namespaces = (await snapshot(request, ['namespaces'])).views.namespaces!.rows as NamespaceRow[]
  const want: [string, string, string][] = [
    ['endpoints', 'Endpoints', 'Endpoints'],
    ['networkpolicies', 'NetworkPolicy', 'NetworkPolicies'],
    ['ingresses', 'Ingress', 'Ingresses'],
  ]
  let seen = 0
  for (const [kindName, kind, plural] of want) {
    const ns = namespaces.find((n) => (n.counts[kindName] ?? 0) > 0)
    if (!ns) continue
    seen++
    await open(page, `#/namespaces/${ns.name}?kinds=${kind}`)
    const card = page.locator(`section[data-kind="${kind}"]`)
    await expect(card).toHaveAttribute('aria-label', plural)
    await expect(page.getByTestId('namespace-kinds').locator(`[data-subtab="${kind}"]`)).toContainText(plural)
  }
  // Every cluster has Endpoints beside its Services; the simulator has all three.
  expect(seen, 'kinds with awkward plurals present').toBeGreaterThan(0)
})

test('a workload opens its details and pods, a pod opens in place, and its owner link leads back (review)', async ({ page, request }) => {
  // A Deployment with pods: the console's own in kind, any in the simulator.
  const namespaces = (await snapshot(request, ['namespaces'])).views.namespaces!.rows as NamespaceRow[]
  const ns = inKind ? '221b' : namespaces.find((n) => (n.counts.deployments ?? 0) > 0 && n.pods > 0)!.name
  const objects = (await snapshot(request, [`ns:${ns}`])).views[`ns:${ns}`]!.rows as ObjectRow[]
  const deployment = objects.find((o) => o.kind === 'Deployment' && /^[1-9]\d*\/\d+ ready/.test(o.status))!
  expect(deployment, `a Deployment with ready pods in ${ns}`).toBeTruthy()
  const details = (await (await request.get(`/api/v1/objects/deployments/${ns}/${deployment.name}`)).json()) as { workload: boolean; pods: { name: string }[]; facts: { label: string; value: string }[] }
  expect(details.workload).toBe(true)
  expect(details.pods.length).toBeGreaterThan(0)

  // The row opens the Deployment's drawer, not just its YAML.
  await open(page, `#/namespaces/${ns}?kinds=Deployment`)
  await page.locator(`[data-object="${deployment.key}"]`).click()
  const panel = page.getByTestId('object-panel')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(deployment.name)
  await expect(panel).toContainText('Deployment in')
  await expect(panel.locator('dt', { hasText: /^Replicas$/ }).locator('+ dd')).toContainText('desired')
  await expect(panel.locator('[data-dtab="pods"]')).toContainText(String(details.pods.length))

  // Its pods, with the same table controls as everywhere else.
  await panel.locator('[data-dtab="pods"]').click()
  await expect(panel.locator('[data-pod]')).toHaveCount(details.pods.length)
  await expect(panel.getByRole('button', { name: /^Columns/ })).toBeVisible()
  await expect(panel.getByRole('button', { name: /^Export/ })).toBeVisible()
  await panel.locator('[data-pod]').first().click()

  // The pod's own drawer, in place; its owner link names the controller at the top of the chain and opens it.
  const pod = page.getByTestId('pod-panel')
  await expect(pod).toBeVisible()
  await expect(page).toHaveURL(/open=Pod%2F/)
  await expect(pod.locator('[data-dtab="logs"]')).toBeVisible()
  await expect(pod.getByTestId('pod-owner-link')).toHaveText(deployment.name)
  await pod.getByTestId('pod-owner-link').click()
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(deployment.name)
  await expect(panel).toContainText('Deployment in')
  await expect(page).toHaveURL(/open=Deployment%2F/)
  await panel.locator('[data-dtab="yaml"]').click()
  await expect(panel.getByTestId('yaml')).toContainText('kind: Deployment')
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)

  // A ReplicaSet's owner link opens its Deployment.
  const replicaSet = objects.find((o) => o.kind === 'ReplicaSet' && o.name.startsWith(deployment.name + '-') && /^[1-9]/.test(o.status))!
  expect(replicaSet, `a live ReplicaSet of ${deployment.name}`).toBeTruthy()
  await open(page, `#/namespaces/${ns}?open=${encodeURIComponent(replicaSet.key)}`)
  await expect(panel).toContainText('ReplicaSet in')
  await expect(panel.getByTestId('object-owner-link')).toHaveText(deployment.name)
  await panel.getByTestId('object-owner-link').click()
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(deployment.name)
  await expect(panel).toContainText('Deployment in')
})

test('an object opens its details, with Secret data replaced by key names and sizes (P2-08)', async ({ page }) => {
  const secret = await cluster.plantedSecret()
  try {
    await open(page, `#/namespaces/${secret.namespace}?kinds=Secret`)
    const row = page.locator(`[data-object="Secret/${secret.name}"]`)
    await expect(row).toBeVisible({ timeout: 15_000 })
    // The table shows keys and sizes, never values.
    await expect(row).toContainText(`token (${PLANTED.length} B)`)
    await row.click()
    const drawer = page.getByTestId('object-panel')
    await expect(drawer).toContainText('Secret in')
    const keys = drawer.getByRole('table', { name: 'Keys' })
    await expect(keys).toContainText('token')
    await expect(keys).toContainText(`${PLANTED.length} B`)
    await expect(drawer.getByTestId('keys-note')).toContainText('never their values')
    await drawer.locator('[data-dtab="yaml"]').click()
    await expect(drawer.getByTestId('yaml')).toContainText('kind: Secret')
    await expect(drawer.getByTestId('yaml')).toContainText(`token: <${PLANTED.length} bytes, hidden by 221B>`)
    await expect(drawer.getByRole('region', { name: 'YAML' })).not.toContainText('managedFields')
    const text = await drawer.textContent()
    expect(text).not.toContain(PLANTED)
    expect(text).not.toContain(Buffer.from(PLANTED).toString('base64'))
    await page.keyboard.press('Escape')
    await expect(drawer).toHaveCount(0)
  } finally {
    await secret.cleanup()
  }
  // Secrets are read by a server flag; the UI says so when they are off.
  if (!inKind) await expect(page.getByRole('link', { name: /^Helm(\s|$)/ })).toBeVisible()
})

test('a namespace’s Helm releases are a tab like its other kinds (asked at the Phase 4 review)', async ({ page, request }) => {
  const releases = (await snapshot(request, ['helm'])).views.helm?.rows as ReleaseRow[] | undefined
  test.skip(!releases?.length, 'no Helm releases (Secrets not read)')
  const r = releases![0]!
  await open(page, `#/namespaces/${encodeURIComponent(r.namespace)}?kinds=${encodeURIComponent('Helm release')}`)
  const card = page.getByTestId('namespace-releases')
  await expect(card.locator('.rk-gh [data-col="chart"]')).toBeVisible()
  await expect(card.getByRole('button', { name: /^Columns/ })).toBeVisible()
  await expect(card.getByRole('button', { name: /^Export/ })).toBeVisible()
  const row = card.locator(`[data-key="${r.key}"]`)
  await expect(row).toContainText(r.name)
  await expect(row).toContainText(`${r.chart}-${r.chartVersion}`)
  await expect(row).toContainText(r.status)
  // A release opens on the Helm tab.
  await row.click()
  await expect(page).toHaveURL(new RegExp(`#/helm/${r.namespace}/${r.name}$`))
})
