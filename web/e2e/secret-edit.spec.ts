// Secret editing (Phase 10, SE-02 to SE-05): an Admin replaces a key blind
// and makes a Secret, written as them; with secrets.reveal off (the
// default here) no value comes back, and the planted value stays planted.
import { cluster, inKind, PLANTED } from './cluster'
import { expect, test } from './test'

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SIM = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'

test.describe.configure({ mode: 'serial' })

test('an Admin replaces a value blind and the cluster changes; revealing is off (SE-02, SE-03)', async ({ page, request }) => {
  test.skip(inKind, 'the simulator keeps the planted Secret')
  const secret = await cluster.plantedSecret()
  const cfg = await (await request.get('/api/v1/secrets/config')).json()
  expect(cfg).toEqual({ read: true, reveal: false, edit: true })
  await page.goto(`/#/namespaces/${secret.namespace}?kinds=Secret`)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.locator(`[data-object="Secret/${secret.name}"]`).click()
  const drawer = page.getByTestId('object-panel')
  await expect(drawer.getByTestId('keys-note')).toContainText('never their values')
  await expect(drawer.getByTestId('keys-note')).toContainText('Revealing is off')
  await expect(drawer.getByTestId('secret-reveal-token')).toHaveCount(0)
  await drawer.getByTestId('secret-edit').click()
  await drawer.getByTestId('secret-replace-token').click()
  await drawer.getByTestId('secret-draft-token').fill('changed-by-e2e')
  await drawer.getByTestId('secret-save').click()
  await expect(drawer.getByTestId('secret-edit')).toBeVisible({ timeout: 10_000 })
  try {
    const live = (await (await request.get(`${SIM}/api/v1/namespaces/${secret.namespace}/secrets/${secret.name}`)).json()) as { data: Record<string, string> }
    expect(live.data.token).toBe(Buffer.from('changed-by-e2e').toString('base64'))
    // No value comes back while revealing is off, and the drawer shows none.
    const refused = await request.get(`/api/v1/secrets/${secret.namespace}/${secret.name}/value?key=token`)
    expect(refused.status()).toBe(403)
    expect(await refused.text()).not.toContain('changed-by-e2e')
    expect(await drawer.textContent()).not.toContain('changed-by-e2e')
    const log = await cluster.serverLog()
    expect(log).not.toContain('changed-by-e2e')
    const audit = JSON.stringify(await cluster.auditLog())
    expect(audit).toContain('secret.update')
    expect(audit).not.toContain('changed-by-e2e')
  } finally {
    // The planted value goes back, for the leak test that follows.
    const back = await request.put(`/api/v1/secrets/${secret.namespace}/${secret.name}`, { data: { set: { token: PLANTED } }, headers: SAME })
    expect(back.status()).toBe(204)
    await secret.cleanup()
  }
})

test('New Secret… makes a Secret with its keys (SE-05)', async ({ page, request }) => {
  test.skip(inKind, 'the simulator serves Secret writes')
  await page.goto('/#/namespaces/default?kinds=Secret')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.getByTestId('secret-new').click()
  const dlg = page.getByTestId('secret-dialog')
  await dlg.getByTestId('secret-new-name').fill('e2e-made')
  await dlg.getByTestId('secret-new-key-0').fill('username')
  await dlg.getByTestId('secret-new-value-0').fill('una')
  await dlg.getByTestId('secret-create').click()
  await expect(dlg).toHaveCount(0)
  try {
    const row = page.locator('[data-object="Secret/e2e-made"]')
    await expect(row).toBeVisible({ timeout: 15_000 })
    await expect(row).toContainText('username (3 B)')
  } finally {
    await request.delete(`${SIM}/api/v1/namespaces/default/secrets/e2e-made`)
  }
})

test('New namespace… makes a namespace and opens it (NS-01)', async ({ page, request }) => {
  test.skip(inKind, 'the simulator serves namespace writes')
  await page.goto('/#/namespaces')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.getByTestId('namespace-new').click()
  const dlg = page.getByTestId('namespace-dialog')
  await dlg.getByTestId('namespace-new-name').fill('e2e-made-ns')
  await dlg.getByTestId('namespace-new-labels').fill('team=e2e')
  await dlg.getByTestId('namespace-create').click()
  await expect(dlg).toHaveCount(0)
  try {
    await expect(page).toHaveURL(/#\/namespaces\/e2e-made-ns/)
    // The simulator pushes no watch event for this write; the object is there.
    const made = await request.get(`${SIM}/api/v1/namespaces/e2e-made-ns`)
    expect(made.status()).toBe(200)
    expect(((await made.json()) as { metadata: { labels?: Record<string, string> } }).metadata.labels?.team).toBe('e2e')
    const audit = JSON.stringify(await cluster.auditLog())
    expect(audit).toContain('namespace.create')
  } finally {
    await request.delete(`${SIM}/api/v1/namespaces/e2e-made-ns`)
  }
})
