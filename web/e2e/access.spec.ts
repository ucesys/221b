// Per-person access (P3-02): what Kubernetes lets a person list is what
// they see, in every tab, in search and in exports; the rest is refused.
// dev@corp.example may list only some namespaces (a limit the simulator
// is told, or RoleBindings in kind); the suite's administrator sees all.
import { readFileSync } from 'node:fs'
import type { Browser, Page } from '@playwright/test'
import { expect, test } from './test'
import { E2E_DEV, E2E_PASSWORD } from './accounts'
import { cluster, inKind, nodeRows, podRows, snapshot } from './cluster'
import type { Snapshot } from '../src/api/types'

/** Signs a fresh context in as an account, through whichever provider the console has. */
async function signIn(browser: Browser, email: string, password: string): Promise<Page> {
  const context = await browser.newContext({ storageState: undefined })
  const page = await context.newPage()
  await page.goto('/#/pods', { waitUntil: 'domcontentloaded' })
  const oidc = page.getByTestId('signin-oidc')
  if (await oidc.count()) {
    await oidc.click()
    await page.waitForURL(/\/oidc\/auth/)
    await page.fill('input[name="email"]', email)
    await page.fill('input[name="password"]', password)
    await page.click('button[type="submit"]')
  } else {
    const form = page.getByTestId('signin-form')
    await form.locator('input[name="email"]').fill(email)
    await form.locator('input[name="password"]').fill(password)
    await form.getByTestId('signin-submit').click()
  }
  return page
}

test.describe.configure({ mode: 'serial' })

test('a person sees only the namespaces Kubernetes lets them list, in every tab, search and export (P3-02)', async ({ browser, request }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const allowed = await cluster.limitDev()
  const all = await snapshot(request, ['pods', 'namespaces', 'nodes'])
  const hidden = podRows(all).find((p) => !allowed.includes(p.namespace))
  expect(hidden, 'a pod outside the person’s namespaces, for the checks').toBeTruthy()
  const shown = podRows(all).find((p) => allowed.includes(p.namespace))
  expect(shown, 'a pod inside them').toBeTruthy()

  const page = await signIn(browser, E2E_DEV, E2E_PASSWORD)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
  const api = page.context().request

  // The API answers the person with their namespaces only, and the totals to match.
  const mine = (await (await api.get('/api/v1/snapshot?views=pods,namespaces,nodes')).json()) as Snapshot
  const myPods = podRows(mine)
  expect(myPods.length).toBeGreaterThan(0)
  for (const p of myPods) expect(allowed, p.key).toContain(p.namespace)
  const myNamespaces = (mine.views.namespaces?.rows ?? []) as { name: string }[]
  expect(myNamespaces.map((n) => n.name).sort()).toEqual(allowed.filter((ns) => (all.views.namespaces?.rows as { name: string }[]).some((n) => n.name === ns)).sort())
  // Nodes are visible to everyone who signs in (221b-nodes, bound to
  // 221b:console-users) on a real cluster; the simulator knows no such group.
  const myNodes = inKind ? nodeRows(all).length : 0
  expect(nodeRows(mine).length).toBe(myNodes)
  expect(mine.counts?.pods).toBe(myPods.length)
  expect(mine.counts?.nodes ?? 0).toBe(myNodes)
  expect(JSON.stringify(mine)).not.toContain(hidden!.name)

  // The Pods tab lists them (the table renders a window of a long list,
  // so the counts are what is checked), and search finds nothing of the rest.
  const count = `${myPods.length} ${myPods.length === 1 ? 'pod' : 'pods'}`
  await page.goto('/#/pods')
  await expect(page.locator('[data-pod]').first()).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('pods-count')).toHaveText(count)
  await page.getByTestId('pod-search').fill(hidden!.name)
  await expect(page.locator('[data-pod]')).toHaveCount(0)
  await expect(page.getByText('No pods match these filters.')).toBeVisible()
  await page.getByTestId('pod-search').fill(shown!.name)
  await expect(page.locator('[data-pod]').first()).toBeVisible()
  await page.getByTestId('pod-search').fill('')
  await expect(page.getByTestId('pods-count')).toHaveText(count)

  // An export carries their pods only.
  await page.getByRole('button', { name: /^Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'JSON' }).click()])
  const exported = readFileSync(await download.path(), 'utf8')
  expect(exported).toContain(shown!.name)
  expect(exported).not.toContain(hidden!.name)

  // The Nodes tab shows the nodes everyone sees (none in the simulator);
  // the Namespaces tab their namespaces.
  await page.goto('/#/nodes')
  await expect(page.locator('[data-node]')).toHaveCount(myNodes)
  if (!inKind) await expect(page.locator('[data-node]')).toHaveCount(0)
  await page.goto('/#/namespaces')
  const list = page.getByTestId('namespace-list')
  // Each row's link is its namespace's name.
  await expect(list.locator('a.rk-rname').first()).toBeVisible({ timeout: 20_000 })
  await expect(list.locator('a.rk-rname')).toHaveCount(myNamespaces.length)
  await expect(list).not.toContainText(hidden!.namespace)

  // What they may not list is refused by name too: details, logs and YAML.
  const [ns, name] = [hidden!.namespace, hidden!.name]
  for (const path of [`/api/v1/pods/${ns}/${name}`, `/api/v1/pods/${ns}/${name}/log`, `/api/v1/yaml/pods/${ns}/${name}`]) {
    const res = await api.get(path)
    expect(res.status(), path).toBe(403)
    expect(await res.text()).toContain('Kubernetes does not let you')
  }
  await page.goto(`/#/pods?open=${ns}/${name}`)
  await expect(page.getByTestId('pod-panel')).toContainText('Kubernetes does not let you')
  // And what they may: the drawer, its logs and YAML, read as them.
  const [okNs, okName] = [shown!.namespace, shown!.name]
  for (const path of [`/api/v1/pods/${okNs}/${okName}`, `/api/v1/yaml/pods/${okNs}/${okName}`]) {
    expect((await api.get(path)).status(), path).toBe(200)
  }
  await page.goto(`/#/pods?open=${okNs}/${okName}`)
  await expect(page.getByTestId('pod-panel')).toContainText(okName)
  await page.context().close()
})

test('the administrator still sees everything, and a change in RBAC reaches a person without a reload (P3-02)', async ({ browser, request }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const allowed = await cluster.limitDev()
  const all = await snapshot(request, ['pods'])
  const hidden = podRows(all).find((p) => !allowed.includes(p.namespace))!
  // The suite's own session is the administrator's: everything, as before.
  expect(podRows(all).some((p) => p.namespace === hidden.namespace)).toBe(true)

  const page = await signIn(browser, E2E_DEV, E2E_PASSWORD)
  await page.goto('/#/pods')
  await expect(page.locator('[data-pod]').first()).toBeVisible({ timeout: 20_000 })
  const before = (await page.getByTestId('pods-count').textContent()) ?? ''
  await page.getByTestId('pod-search').fill(hidden.name)
  await expect(page.locator('[data-pod]')).toHaveCount(0)

  // Kubernetes changes its mind: the person's stream starts over with
  // what they may see now, within the console's access-ttl.
  await cluster.allowDev(hidden.namespace)
  await expect(page.locator('[data-pod]')).toHaveCount(1, { timeout: 90_000 })
  await page.getByTestId('pod-search').fill('')
  await expect(page.getByTestId('pods-count')).not.toHaveText(before)
  await page.context().close()
  await cluster.limitDev()
})

test.afterAll(async () => {
  if (!inKind) await cluster.limitDev()
})
