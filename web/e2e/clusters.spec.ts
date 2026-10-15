// Clusters (P3-04 to P3-09): the cluster menu, the connection editor and
// its test, the cluster page, credentials kept in Secrets and never shown
// again, and removal. The suite's session is an administrator's; a viewer
// signs in apart to see the menu without Edit, Add or Remove (P3-03).
import type { APIRequestContext, Browser, Page } from '@playwright/test'
import { expect, test } from './test'
import { E2E_PASSWORD, E2E_VIEWER } from './accounts'
import { cluster, inKind } from './cluster'

const NAME = 'second'
// Every browser's run adds its own connection, so the runs do not trip
// over each other; the name must slug to something unique per browser.
const name = (browser: string) => `${NAME}-${browser}`

async function removeIfThere(request: APIRequestContext, id: string) {
  const res = await request.delete(`/api/v1/clusters/${id}`, { headers: { 'Sec-Fetch-Site': 'same-origin' } })
  expect([200, 404]).toContain(res.status())
}

/** Signs a fresh context in as an account, through whichever provider the console has. */
async function signIn(browser: Browser, email: string, password: string): Promise<Page> {
  const context = await browser.newContext({ storageState: undefined })
  const page = await context.newPage()
  await page.goto('/#/nodes', { waitUntil: 'domcontentloaded' })
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

test('the cluster menu lists the console’s own cluster, and the API lists it without credentials (P3-04, P3-07)', async ({ page, request, browserName }) => {
  await removeIfThere(request, name(browserName))
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
  const list = (await (await request.get('/api/v1/clusters')).json()) as { clusters: Record<string, unknown>[] }
  const local = list.clusters.find((c) => c.id === 'local')!
  expect(local).toMatchObject({ local: true, mode: 'read' })
  expect(typeof local.server).toBe('string')
  // Nothing secret about the local connection leaves the server.
  expect(JSON.stringify(local)).not.toMatch(/token|kubeconfig|BEGIN/i)

  await page.getByTestId('cluster').click()
  const menu = page.getByTestId('cluster-menu')
  await expect(menu).toBeVisible()
  const row = menu.locator('[data-testid=cluster-row][data-cluster=local]')
  await expect(row).toContainText('here')
  await expect(row).toContainText('Connected')
  await expect(row.locator('[aria-current=page]')).toHaveCount(1)
  await expect(menu.getByTestId('cluster-add')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
})

test('the editor reports a bad address and a wrong token at the step that fails; a saved connection that cannot sign in says so (P3-05, P3-06, P3-07)', async ({ page, request, browserName }) => {
  const other = await cluster.other()
  const id = name(browserName)
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
  await page.getByTestId('cluster').click()
  await page.getByTestId('cluster-add').click()
  const editor = page.getByTestId('cluster-editor')
  await expect(editor).toBeVisible()
  await expect(editor.getByTestId('cluster-name')).toBeFocused()

  // Nothing filled in: the form says what is missing before asking the server.
  await editor.getByTestId('cluster-save').click()
  await expect(editor.getByTestId('cluster-error')).toHaveText('Give the cluster a name.')
  await editor.getByTestId('cluster-name').fill(id)
  await editor.getByTestId('cluster-save').click()
  await expect(editor.getByTestId('cluster-error')).toContainText('The API server address starts with https://')

  // An address nothing answers on.
  await editor.getByTestId('cluster-server').fill('http://127.0.0.1:9')
  await editor.locator('[aria-label="Certificate authority"] [data-value=system]').click()
  await editor.getByTestId('cluster-token').fill('not-a-token')
  await expect(editor.getByTestId('cred-note')).toContainText('isn’t a service account token')
  await editor.getByTestId('cluster-token').fill(other.token)
  await expect(editor.getByTestId('cred-note')).toContainText('Token for')
  await editor.getByTestId('cluster-test').click()
  await expect(editor.locator('[data-testid=test-steps][data-done=true]')).toBeVisible({ timeout: 30_000 })
  await expect(editor.locator('[data-testid=test-step][data-step=Address]')).toHaveAttribute('data-tone', 'err')
  await expect(editor.locator('[data-testid=test-step][data-step=TLS]')).toHaveAttribute('data-tone', 'muted')
  await expect(editor.locator('[data-testid=test-step][data-step=Address]')).toContainText(/refused|answer|reach/i)
  await expect(editor.getByTestId('test-summary')).toContainText('Can’t connect: the address step failed.')

  // The right address with a wrong token: sign-in fails, the steps before pass.
  await editor.getByTestId('cluster-server').fill(other.server)
  if (other.ca === 'pem') {
    await editor.locator('[aria-label="Certificate authority"] [data-value=pem]').click()
    await editor.getByTestId('cluster-ca-pem').fill(other.caPem)
  }
  await editor.getByTestId('cluster-token').fill(other.token.slice(0, -4) + 'xxxx')
  await editor.getByTestId('cluster-test').click()
  await expect(editor.locator('[data-testid=test-steps][data-done=true]')).toBeVisible({ timeout: 30_000 })
  await expect(editor.locator('[data-testid=test-step][data-step=Address]')).toHaveAttribute('data-tone', 'ok')
  await expect(editor.locator('[data-testid=test-step][data-step=Sign-in]')).toHaveAttribute('data-tone', 'err')
  await expect(editor.locator('[data-testid=test-step][data-step=Kubernetes]')).toHaveAttribute('data-tone', 'muted')

  // Saved anyway (the cluster may be down for a while): it opens, and says it cannot connect.
  await editor.locator('[aria-label="Environment"] [data-value=staging]').click()
  await editor.getByTestId('cluster-description').fill('Added by the end-to-end suite')
  await editor.getByTestId('cluster-save').click()
  await expect(page).toHaveURL(new RegExp(`#/c/${id}/alerts$`))
  await expect(page.getByTestId('cluster')).toContainText(id)
  await expect(page.getByTestId('cluster-state')).toHaveText('Disconnected', { timeout: 30_000 })
  await expect(page.getByTestId('banner-link')).toHaveText('About the connection')
  // The home page lists it beside the console's own, and says it cannot connect.
  await page.getByTestId('home-link').click()
  const homeCard = page.locator(`[data-testid=home-card][data-cluster="${id}"]`)
  await expect(homeCard.getByTestId('home-health')).toHaveText('Can’t connect', { timeout: 30_000 })
  await expect(homeCard.getByRole('link', { name: 'See its connection' })).toHaveAttribute('href', `#/c/${id}/cluster`)
  await expect(page.locator('[data-testid=home-card][data-cluster=local]')).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(new RegExp(`#/c/${id}/alerts$`))
  await page.getByTestId('banner-link').click()
  const card = page.getByTestId('cluster-page')
  await expect(card).toBeVisible()
  await expect(card.getByTestId('cluster-problem')).toContainText(/401|Unauthorized/i)
  expect(await cluster.connectionSecret(id), 'the connection has its Secret').toBe(true)
  const saved = (await (await request.get(`/api/v1/clusters/${id}`)).json()) as Record<string, unknown>
  expect(saved).toMatchObject({ id, state: 'disconnected' })

  // Fix the connection: the token is replaced, nothing else retyped.
  await card.getByTestId('cluster-fix').click()
  await expect(editor.getByTestId('cred-saved')).toContainText('Token for')
  await editor.getByTestId('cred-replace').click()
  await editor.getByTestId('cluster-token').fill(other.token)
  await editor.getByTestId('cluster-test').click()
  await expect(editor.locator('[data-testid=test-steps][data-done=true]')).toBeVisible({ timeout: 30_000 })
  for (const step of ['Address', 'Sign-in', 'Kubernetes', 'Permissions']) {
    await expect(editor.locator(`[data-testid=test-step][data-step=${step}]`), step).toHaveAttribute('data-tone', /ok|warn/)
  }
  await expect(editor.locator('[data-testid=test-step][data-step=Kubernetes]')).toContainText(`${other.nodes} node`)
  await editor.getByTestId('rbac-toggle').click()
  await expect(editor.getByTestId('rbac-yaml')).toContainText('kind: ClusterRoleBinding')
  await editor.getByTestId('cluster-save').click()
  await expect(editor).toHaveCount(0)
  await expect(card.getByTestId('cluster-page-state')).toContainText('Connected', { timeout: 30_000 })
  await expect(card.getByTestId('cluster-problem')).toHaveCount(0)
})

test('a connected cluster shows its data under its own routes, and switching keeps the tab and filters (P3-04, P3-07, P3-09)', async ({ page, request, browserName }) => {
  const other = await cluster.other()
  const id = name(browserName)
  await page.goto(`/#/c/${id}/nodes`)
  await expect(page.getByTestId('cluster')).toContainText(id)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 30_000 })
  await expect(page.locator('[data-node]')).toHaveCount(other.nodes, { timeout: 30_000 })
  await expect(page.getByTestId('cluster-env')).toHaveText('staging')

  // The credentials are kept in a Secret of the console's namespace and never come back.
  const saved = (await (await request.get(`/api/v1/clusters/${id}`)).json()) as Record<string, unknown>
  expect(saved).toMatchObject({ id, name: id, env: 'staging', auth: 'token', mode: 'read', description: 'Added by the end-to-end suite', state: 'connected' })
  expect((saved.cred as { who?: string }).who).toBe(other.who)
  expect(JSON.stringify(saved)).not.toContain(other.token)
  expect(JSON.stringify(saved)).not.toContain(other.token.slice(-20))
  expect(await cluster.connectionSecret(id), 'the connection has its Secret').toBe(true)
  // Its data answers under its own routes, and the local cluster under the short ones.
  const otherSnap = (await (await request.get(`/api/v1/clusters/${id}/snapshot?views=nodes`)).json()) as { cluster: { name: string } }
  expect(otherSnap.cluster.name).toBe(id)
  const localSnap = (await (await request.get('/api/v1/snapshot?views=nodes')).json()) as { cluster: { name: string } }
  expect(localSnap.cluster.name).not.toBe(id)
  expect((await request.get('/api/v1/clusters/nowhere/snapshot')).status()).toBe(404)

  // Switching keeps the open tab and its filters (P3-04).
  await page.goto(`/#/c/${id}/pods?status=Running`)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 30_000 })
  await expect(page.locator('[data-pod]').first()).toBeVisible({ timeout: 30_000 })
  await page.getByTestId('cluster').click()
  await page.locator('[data-testid=cluster-row][data-cluster=local] .rk-clpick').click()
  await expect(page).toHaveURL(/#\/pods\?status=Running$/)
  await expect(page.getByTestId('cluster')).not.toContainText(id)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 30_000 })
  await page.getByTestId('cluster').click()
  await page.locator(`[data-testid=cluster-row][data-cluster=${id}] .rk-clpick`).click()
  await expect(page).toHaveURL(new RegExp(`#/c/${id}/pods\\?status=Running$`))
  await expect(page.getByTestId('cluster')).toContainText(id)
  // A pod's drawer reads from that cluster too.
  await page.locator('[data-pod]').first().click()
  await expect(page.getByTestId('pod-panel')).toBeVisible()
})

test('the cluster page shows the connection, tests it again, and the editor keeps saved credentials (P3-06, P3-07)', async ({ page, request, browserName }) => {
  const id = name(browserName)
  await page.goto(`/#/c/${id}/cluster`)
  const card = page.getByTestId('cluster-page')
  await expect(card).toBeVisible({ timeout: 20_000 })
  await expect(card.getByRole('heading', { level: 1 })).toHaveText(id)
  await expect(card.getByTestId('cluster-page-state')).toContainText('Connected', { timeout: 30_000 })
  await expect(card).toContainText('Service account token')
  await expect(card).toContainText('Read only')
  await expect(card).toContainText('Staging')
  // No tab is open on this page.
  await expect(page.locator('.rk-tabs [aria-current=page]')).toHaveCount(0)

  await card.getByTestId('cluster-retest').click()
  await expect(page.locator('[data-testid=test-steps][data-done=true]')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('test-summary')).toContainText('Connected')
  await expect(card).toContainText('Last tested')
  // The test is kept with the connection.
  const saved = (await (await request.get(`/api/v1/clusters/${id}`)).json()) as { lastTest?: { tone: string } }
  expect(saved.lastTest?.tone).toMatch(/ok|warn/)

  // A change that keeps the credentials: the editor shows what it has, not the token.
  await card.getByTestId('cluster-edit').click()
  const editor = page.getByTestId('cluster-editor')
  await expect(editor.getByTestId('cred-saved')).toContainText('Token for')
  await expect(editor.getByTestId('cluster-token')).toHaveCount(0)
  await expect(editor.locator('input[type=password]')).toHaveCount(0)
  await editor.getByTestId('cluster-description').fill('Changed by the end-to-end suite')
  await editor.locator('[aria-label="What the console may do"] [data-value=write]').click()
  await editor.getByTestId('cluster-save').click()
  await expect(editor).toHaveCount(0)
  await expect(card).toContainText('Changed by the end-to-end suite')
  await expect(card).toContainText('Read and change')
  await expect(card.getByTestId('cluster-page-state')).toContainText('Connected', { timeout: 30_000 })
  const after = (await (await request.get(`/api/v1/clusters/${id}`)).json()) as { mode: string; cred: { who?: string } }
  expect(after.mode).toBe('write')
  expect(after.cred.who, 'the credential was kept').toBe((await cluster.other()).who)

  // Replacing the credentials asks for them, and "Keep the saved credentials" backs out.
  await card.getByTestId('cluster-edit').click()
  await editor.getByTestId('cred-replace').click()
  await expect(editor.getByTestId('cluster-token')).toBeVisible()
  await editor.getByRole('button', { name: 'Keep the saved credentials' }).click()
  await expect(editor.getByTestId('cred-saved')).toBeVisible()
  await page.keyboard.press('Escape')
})

test('a viewer switches clusters but cannot add, edit or remove them (P3-03)', async ({ browser, request, browserName }) => {
  const me = (await (await request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const id = name(browserName)
  const page = await signIn(browser, E2E_VIEWER, E2E_PASSWORD)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
  await page.getByTestId('cluster').click()
  const menu = page.getByTestId('cluster-menu')
  await expect(menu.locator(`[data-testid=cluster-row][data-cluster=${id}]`)).toBeVisible()
  await expect(menu.getByTestId('cluster-add')).toHaveCount(0)
  await menu.locator(`[data-testid=cluster-row][data-cluster=${id}] .rk-clpick`).click()
  // The open tab comes along; from the home page, a cluster opens on its first tab.
  await expect(page).toHaveURL(new RegExp(`#/c/${id}/(nodes|alerts)$`))
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 30_000 })
  await page.goto(`/#/c/${id}/cluster`)
  const card = page.getByTestId('cluster-page')
  await expect(card).toBeVisible()
  await expect(card.getByTestId('cluster-edit')).toHaveCount(0)
  await expect(card.getByTestId('cluster-retest')).toHaveCount(0)
  const api = page.context().request
  const headers = { 'Sec-Fetch-Site': 'same-origin' }
  expect((await api.post('/api/v1/clusters', { data: { name: 'x' }, headers })).status()).toBe(403)
  expect((await api.put(`/api/v1/clusters/${id}`, { data: { name: id }, headers })).status()).toBe(403)
  expect((await api.post(`/api/v1/clusters/${id}/test`, { headers })).status()).toBe(403)
  expect((await api.delete(`/api/v1/clusters/${id}`, { headers })).status()).toBe(403)
  expect((await api.get('/api/v1/clusters')).status()).toBe(200)
  await page.context().close()
})

test('a kubeconfig signs in too, with the context picked and the address taken from the file (P3-05, P3-09)', async ({ request, browserName }) => {
  const other = await cluster.other()
  const id = `${name(browserName)}-kc`
  await removeIfThere(request, id)
  // Two contexts, the current one pointing nowhere: the picked one is used.
  const ca = other.ca === 'pem' ? `      certificate-authority-data: ${Buffer.from(other.caPem).toString('base64')}\n` : ''
  const kubeconfig = ['apiVersion: v1', 'kind: Config', 'clusters:', '  - name: other', '    cluster:', `      server: ${other.server}`, ca.trimEnd(), '  - name: nowhere', '    cluster:', '      server: https://nowhere.invalid:6443', 'contexts:', '  - name: e2e', '    context:', '      cluster: other', '      user: console', '  - name: elsewhere', '    context:', '      cluster: nowhere', '      user: console', 'current-context: elsewhere', 'users:', '  - name: console', '    user:', `      token: ${other.token}`, '']
    .filter((line) => line !== '')
    .join('\n')
  const draft = { name: id, env: 'development', description: 'Added by the end-to-end suite, from a kubeconfig', server: '', ca: other.ca, auth: 'kubeconfig', kubeconfig, context: 'e2e', mode: 'read' }
  const added = await request.post('/api/v1/clusters', { data: draft, headers: { 'Sec-Fetch-Site': 'same-origin' } })
  expect(added.status(), await added.text()).toBe(201)
  const summary = (await added.json()) as Record<string, unknown>
  expect(summary).toMatchObject({ id, auth: 'kubeconfig', server: other.server, cred: { context: 'e2e', cluster: 'other', who: 'console' } })
  expect(JSON.stringify(summary)).not.toContain(other.token)

  // It connects, serves the other cluster's nodes, and passes its test.
  await expect.poll(async () => ((await (await request.get(`/api/v1/clusters/${id}`)).json()) as { state: string }).state, { timeout: 30_000 }).toBe('connected')
  const nodes = async () => ((await (await request.get(`/api/v1/clusters/${id}/snapshot?views=nodes`)).json()) as { views: { nodes?: { rows: unknown[] } } }).views.nodes?.rows.length ?? 0
  await expect.poll(nodes, { timeout: 30_000 }).toBe(other.nodes)
  const tested = (await (await request.post(`/api/v1/clusters/${id}/test`, { headers: { 'Sec-Fetch-Site': 'same-origin' } })).json()) as { tone: string; steps: { k: string; tone: string; t: string }[] }
  expect(tested.tone, JSON.stringify(tested)).toMatch(/ok|warn/)
  expect(tested.steps.find((s) => s.k === 'Kubernetes')?.t).toContain(`${other.nodes} node`)
  const removed = await request.delete(`/api/v1/clusters/${id}`, { headers: { 'Sec-Fetch-Site': 'same-origin' } })
  expect(await removed.json()).toEqual({ removed: true })
})

test('removing a production cluster asks for its name, and deletes its Secret (P3-08, P3-10)', async ({ page, request, browserName }) => {
  const id = name(browserName)
  await page.goto(`/#/c/${id}/cluster`)
  const card = page.getByTestId('cluster-page')
  await expect(card).toBeVisible({ timeout: 20_000 })
  await card.getByTestId('cluster-edit').click()
  const editor = page.getByTestId('cluster-editor')
  await editor.locator('[aria-label="Environment"] [data-value=production]').click()
  await expect(editor).toContainText('Removing a production cluster asks you to type its name.')
  await editor.getByTestId('cluster-save').click()
  await expect(editor).toHaveCount(0)
  await expect(card).toContainText('Production')

  await card.getByTestId('cluster-edit').click()
  await editor.getByTestId('cluster-remove').click()
  await expect(editor.getByTestId('cluster-remove')).toBeDisabled()
  await editor.getByTestId('cluster-remove-name').fill('not-it')
  await expect(editor.getByTestId('cluster-remove')).toBeDisabled()
  await editor.getByTestId('cluster-remove-name').fill(id)
  await expect(editor.getByTestId('cluster-remove')).toBeEnabled()
  await editor.getByTestId('cluster-remove').click()
  await expect(editor).toHaveCount(0)
  // Back on the home page, and the connection is gone with its Secret.
  await expect(page).toHaveURL(/#\/$/)
  await expect(page.locator(`[data-testid=home-card][data-cluster="${id}"]`)).toHaveCount(0)
  await expect(page.getByTestId('cluster')).not.toContainText(id)
  expect((await request.get(`/api/v1/clusters/${id}`)).status()).toBe(404)
  expect((await request.get(`/api/v1/clusters/${id}/snapshot`)).status()).toBe(404)
  await expect.poll(() => cluster.connectionSecret(id), { timeout: 15_000 }).toBe(false)

  // The audit log tells the whole story, naming the administrator and never the token.
  const other = await cluster.other()
  await expect.poll(async () => (await cluster.auditLog()).filter((e) => e.action === 'cluster.remove' && e.cluster === id).length, { timeout: 15_000 }).toBeGreaterThan(0)
  const records = (await cluster.auditLog()).filter((e) => e.cluster === id)
  for (const action of ['cluster.test', 'cluster.add', 'cluster.update', 'cluster.remove']) {
    const r = records.find((e) => e.action === action && e.outcome === 'ok')
    expect(r, action).toBeTruthy()
    expect((r!.who as { email?: string }).email, action).toBeTruthy()
  }
  expect(JSON.stringify(records)).not.toContain(other.token)
  if (inKind) expect(JSON.stringify(records)).not.toContain(other.caPem.trim().split('\n')[1])
})
