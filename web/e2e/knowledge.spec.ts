// Mycroft's knowledge (Phase 12, KN-01 to KN-18): Settings › Knowledge
// lists the sources and keeps the wiki in the temporary ConfigMap store
// until another is chosen (CF-57), saying it is temporary;
// a source's token goes in and never comes back from any endpoint; the
// webhook refuses a call without its token; and Settings › Issues has the
// Confluence publisher's part. Cases › Mycroft (Phase 12, wave 5, MY-01
// to MY-10): its sub-tabs and addresses, the Wiki in the default store
// (never "Loading…" for good), and Add with a store (the simulator's
// fake S3 bucket): a note, a CSV document, and an address the console
// does not read.
import { expect, test } from './test'
import { cluster, inKind } from './cluster'

const TOKEN = 'e2e-confluence-read-token-7f3a'

test('Settings › Knowledge takes a source whose token never comes back, and keeps the wiki in the temporary ConfigMap store by default', async ({ page }) => {
  await page.goto('/#/admin/settings/knowledge')
  const section = page.getByTestId('knowledge-section')
  await expect(section).toBeVisible({ timeout: 20_000 })
  await expect(section.locator('[data-testid=knowledge-source][data-source=console]')).toBeVisible()
  await expect(section.locator('[data-testid=knowledge-source][data-source=documents]')).toBeVisible()
  // The temporary ConfigMap store is the default (CF-57), and says so.
  await expect(page.getByTestId('knowledge-configmap-store')).toContainText('temporary, for testing — will be removed; move to Git or S3 before relying on it')
  await expect(page.getByTestId('knowledge-store-configmap')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('knowledge-no-store')).toHaveCount(0)

  await page.getByTestId('knowledge-add').click()
  const drawer = page.getByTestId('knowledge-source-drawer')
  await drawer.getByTestId('knowledge-source-name').fill('E2E space')
  await drawer.getByTestId('knowledge-source-url').fill('https://e2e.atlassian.net')
  await drawer.getByTestId('knowledge-source-email').fill('e2e@example.com')
  await drawer.getByTestId('knowledge-source-space').fill('E2E')
  await drawer.getByTestId('knowledge-source-token').fill(TOKEN)
  await drawer.getByTestId('knowledge-source-save').click()
  const row = section.locator('[data-testid=knowledge-source][data-source=e2e-space]')
  await expect(row).toContainText('token in a Secret')
  for (const path of ['/api/v1/knowledge/settings', '/api/v1/knowledge/status']) {
    const body = await page.evaluate(async (p) => (await fetch(p)).text(), path)
    expect(body).not.toContain(TOKEN)
  }
  await expect.poll(async () => (await cluster.auditLog()).some((r) => r.action === 'knowledge.source'), { timeout: 15_000 }).toBe(true)
  expect(JSON.stringify(await cluster.auditLog())).not.toContain(TOKEN)

  // The webhook wants its token, not a session.
  const hook = await page.request.post('/api/v1/knowledge/hook/wiki', { data: {} })
  expect(hook.status()).toBe(401)

  // Removed again, for the other tests.
  await row.getByText('Edit…').click()
  await page.getByTestId('knowledge-source-remove').click()
  await expect(row).toHaveCount(0)

  // The Confluence publisher's part of Settings › Issues.
  await page.locator('[data-testid=settings-section][data-section=issues]').click()
  await expect(page.getByTestId('confluence-settings')).toContainText('Publish the known issues to Confluence')
  await expect(page.getByTestId('confluence-token-state')).toContainText('No write token saved.')
})

// --- Mycroft tab (Phase 12, wave 5) ---

const SIM = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'

test('Cases › Mycroft has Wiki, Known issues and Exceptions (no RCAs: an RCA is part of its case); the Wiki is kept in the default ConfigMap store', async ({ page }) => {
  await page.goto('/#/cases/mycroft')
  const tab = page.getByTestId('mycroft-tab')
  await expect(tab).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('mycroft-sub-wiki')).toHaveAttribute('aria-pressed', 'true')
  // The bug of the deployed console: "Loading…" for good. The wiki is
  // kept in the temporary ConfigMap store by default (CF-57): no banner
  // asks for a store, and Add is open.
  await expect(page.getByTestId('knowledge-pages')).not.toContainText('Loading…', { timeout: 15_000 })
  await expect(page.getByTestId('wiki-no-store')).toHaveCount(0)
  await page.getByTestId('knowledge-view-add').click()
  await expect(page).toHaveURL(/#\/cases\/mycroft\/wiki\/add$/)
  await expect(page.getByTestId('wiki-add-refused')).toHaveCount(0)
  await page.getByTestId('knowledge-view-sources').click()
  await expect(page.getByTestId('wiki-sources').locator('[data-testid=wiki-source][data-source=added]')).toContainText('Added in Mycroft')

  for (const [sub, url, view] of [
    ['known', /#\/cases\/mycroft\/known$/, 'known-tab'],
    ['exceptions', /#\/cases\/mycroft\/exceptions$/, 'knowledge-exceptions'],
    ['wiki', /#\/cases\/mycroft\/wiki$/, 'knowledge-tab'],
  ] as const) {
    await page.getByTestId(`mycroft-sub-${sub}`).click()
    await expect(page).toHaveURL(url)
    await expect(page.getByTestId(`mycroft-sub-${sub}`)).toHaveAttribute('aria-pressed', 'true')
    if (sub !== 'known') await expect(page.getByTestId(view)).toBeVisible()
  }
  await expect(page.getByTestId('mycroft-sub-rcas')).toHaveCount(0)
  // The old addresses open the new places. Mycroft › RCAs' old addresses, and the wiki's before it, open the Inbox showing all, closed cases too: past RCAs are found by searching them.
  for (const old of ['/#/cases/mycroft/rcas', '/#/cases/mycroft/rcas/RCA-1', '/#/cases/knowledge/rcas/RCA-1']) {
    await page.goto(old)
    await expect(page.getByTestId('inbox-show-all')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByTestId('mycroft-tab')).toHaveCount(0)
  }
  await page.goto('/#/cases/knowledge/exceptions/EX-999')
  await expect(page.getByTestId('mycroft-sub-exceptions')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('knowledge-exceptions')).toContainText('There is no exception EX-999.')
  await page.goto('/#/cases/knowledge/drafts')
  await expect(page.getByTestId('mycroft-sub-wiki')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('knowledge-view-drafts')).toHaveAttribute('aria-pressed', 'true')
})

test('Mycroft › Wiki › Add with a store: a note and a CSV become sources the wiki quotes; a loopback address is refused', async ({ page }) => {
  test.skip(inKind, 'the fake S3 bucket is the simulator’s')
  test.setTimeout(120_000)
  await page.goto('/#/cases/mycroft/wiki/add')
  await expect(page.getByTestId('wiki-add')).toBeVisible({ timeout: 20_000 })
  const api = (path: string, method = 'GET', body?: unknown) =>
    page.evaluate(
      async ([p, m, b]) => {
        const r = await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) })
        return { status: r.status, body: await r.text() }
      },
      [path, method, body] as const,
    )
  // The wiki is kept in the simulator's bucket for this test.
  const store = { kind: 's3', endpoint: SIM, region: 'us-east-1', bucket: 'sim-wiki', prefix: 'e2e/', pathStyle: true, credentials: 'keys' }
  const saved = await api('/api/v1/knowledge/store', 'PUT', { store, keys: { accessKeyId: 'sim-access-key', secretAccessKey: 'sim-secret-key' } })
  expect(saved.status, saved.body).toBe(200)
  try {
    await page.reload()
    await expect(page.getByTestId('wiki-add-refused')).toHaveCount(0, { timeout: 15_000 })
    await expect(page.getByTestId('wiki-no-store')).toHaveCount(0)

    // A note, with a password that never reaches the wiki.
    await page.getByTestId('wiki-add-title').fill('E2E zephyr backups')
    await page.getByTestId('wiki-add-text').fill('The zephyr backups run nightly on nas-7 and are kept for 30 days.\n\npassword=e2e-hunter2-secret-value')
    await page.getByTestId('wiki-add-submit').click()
    await expect(page.getByTestId('wiki-add-result')).toContainText('“E2E zephyr backups” was added.')

    // A CSV document.
    await page.getByTestId('wiki-add-kind-document').click()
    await page.getByTestId('wiki-add-file').setInputFiles({ name: 'e2e-quokka-hosts.csv', mimeType: 'text/csv', buffer: Buffer.from('host,address,role\nquokka-1,10.9.0.1,cache\n') })
    await expect(page.getByTestId('wiki-add-files')).toContainText('e2e-quokka-hosts.csv')
    await page.getByTestId('wiki-add-submit').click()
    await expect(page.getByTestId('wiki-add-result')).toContainText('“e2e-quokka-hosts.csv” was added.')

    // An address inside the console's own pod is not read.
    await page.getByTestId('wiki-add-kind-url').click()
    await page.getByTestId('wiki-add-url').fill('http://127.0.0.1:8080/')
    await page.getByTestId('wiki-add-submit').click()
    await expect(page.getByTestId('wiki-add-error')).toContainText('does not read loopback')

    // Both are read, listed under Sources, and quoted by the search.
    await expect
      .poll(async () => {
        const w = JSON.parse((await api('/api/v1/knowledge/wiki')).body) as { added: { title: string; lastRead?: string }[] }
        return w.added.filter((a) => a.lastRead && /zephyr|quokka/.test(a.title)).length
      }, { timeout: 60_000 })
      .toBe(2)
    await page.getByTestId('knowledge-view-sources').click()
    const added = page.getByTestId('wiki-added')
    await expect(added.getByTestId('wiki-added-item').filter({ hasText: 'E2E zephyr backups' })).toContainText('read ')
    await expect(added.getByTestId('wiki-added-item').filter({ hasText: 'e2e-quokka-hosts.csv' })).toBeVisible()
    await page.getByTestId('knowledge-view-pages').click()
    await page.getByTestId('knowledge-search').fill('zephyr nightly')
    await expect(page.getByTestId('knowledge-hits')).toContainText('E2E zephyr backups')
    await page.getByTestId('knowledge-search').fill('quokka cache')
    await expect(page.getByTestId('knowledge-hits')).toContainText('quokka-1 | 10.9.0.1 | cache')
    const search = await api('/api/v1/knowledge/search?q=zephyr')
    expect(search.body).not.toContain('e2e-hunter2-secret-value')
    await expect.poll(async () => (await cluster.auditLog()).some((r) => r.action === 'knowledge.add'), { timeout: 15_000 }).toBe(true)
    expect(JSON.stringify(await cluster.auditLog())).not.toContain('e2e-hunter2-secret-value')

    // Removed again, from Sources.
    await page.getByTestId('knowledge-view-sources').click()
    for (const title of ['E2E zephyr backups', 'e2e-quokka-hosts.csv']) {
      await added.getByTestId('wiki-added-item').filter({ hasText: title }).getByTestId('wiki-added-remove').click()
      await page.getByTestId('confirm-ok').click()
      await expect(added.getByTestId('wiki-added-item').filter({ hasText: title })).toHaveCount(0)
    }
  } finally {
    // Mycroft's drafts, if it wrote any, are rejected, and the wiki is
    // kept in the default store again, for the other tests.
    const drafts = JSON.parse((await api('/api/v1/knowledge/drafts')).body) as { runs?: { id: string }[] }
    for (const r of drafts.runs ?? []) await api(`/api/v1/knowledge/drafts/${r.id}/reject`, 'POST', { comment: 'e2e' })
    const back = await api('/api/v1/knowledge/store', 'PUT', { store: { kind: '' }, keys: {} })
    expect(back.status, back.body).toBe(200)
  }
})

// Mycroft › Exceptions (build 14): New exception made by hand, shown with
// its Justification, Cluster by name, Object and Validation rule with what
// it means; turned off it is Inactive, saying by whom, and Make active
// again brings it back, refused while another active exception covers the
// same cluster, object and rule. The object is suggested as it is typed.
test('Mycroft › Exceptions: New exception, Inactive with why, and Make active again (refused while another covers it)', async ({ page }) => {
  const SAME = { 'Sec-Fetch-Site': 'same-origin' }
  const stamp = Date.now().toString(36)
  const object = `e2e-${stamp}/Deployment/web`
  await page.goto('/#/cases/mycroft/exceptions')
  await expect(page.getByTestId('knowledge-exceptions')).toBeVisible({ timeout: 20_000 })
  const listed = (await (await page.request.get('/api/v1/cases/exceptions')).json()) as { clusters?: { id: string; name: string }[] }
  const where = listed.clusters?.[0]
  expect(where, 'the console offers a cluster to New exception').toBeTruthy()

  await page.getByTestId('exceptions-new').click()
  const form = page.getByTestId('exception-new')
  await expect(form.getByTestId('exception-new-save')).toBeDisabled()
  await form.getByTestId('exception-new-cluster').selectOption(where!.id)
  await form.getByTestId('exception-new-object').fill(`deploy/e2e-${stamp}/web`)
  await form.getByTestId('exception-new-rule').fill('pod.root-user')
  await form.getByTestId('exception-new-reason').fill('The image needs root; tracked elsewhere.')
  await form.getByTestId('exception-new-save').click()
  await expect(form).toHaveCount(0)
  await expect(page).toHaveURL(/#\/cases\/mycroft\/exceptions\/EX-\d+$/)
  const first = decodeURIComponent(page.url().split('/').pop()!)
  const drawer = page.getByTestId('exception-drawer')
  await expect(drawer).toBeVisible()
  const details = drawer.getByTestId('exception-details')
  await expect(details).toContainText('Justification')
  await expect(details).toContainText('The image needs root; tracked elsewhere.')
  await expect(details.getByTestId('exception-cluster-name')).toHaveText(where!.name)
  await expect(details.getByTestId('exception-cluster-name')).toHaveAttribute('title', where!.id)
  await expect(details).toContainText(object)
  await expect(details.getByTestId('exception-rule')).toHaveText('pod.root-user · a container runs as root')
  await expect(details).not.toContainText('Accepted')

  // The same cluster, object and rule again: refused while it is active.
  const again = await page.request.post('/api/v1/cases/exceptions', { headers: SAME, data: { cluster: where!.id, resource: object, check: 'pod.root-user', reason: 'again' } })
  expect(again.status()).toBe(409)

  // Turned off: Inactive, saying by whom; then made active again.
  await drawer.getByTestId('exception-drawer-remove').click()
  await page.getByTestId('confirm-ok').click()
  await expect(drawer.getByTestId('exception-drawer-inactive')).toContainText('turned off by')
  await expect(drawer.getByTestId('exception-drawer-state')).toHaveText('Inactive')
  await drawer.getByTestId('exception-drawer-reactivate').click()
  await page.getByTestId('confirm-ok').click()
  await expect(drawer.getByTestId('exception-drawer-state')).toHaveText('Active')
  const back = (await (await page.request.get(`/api/v1/cases/exceptions/${first}`)).json()) as { state: string; removedBy?: string }
  expect(back.state).toBe('active')
  expect(back.removedBy).toBeUndefined()

  // Turned off again, and another made for the same: the first may not come back while the second covers it.
  expect((await page.request.delete(`/api/v1/cases/exceptions/${first}`, { headers: SAME })).status()).toBeLessThan(300)
  const second = await page.request.post('/api/v1/cases/exceptions', { headers: SAME, data: { cluster: where!.id, resource: object, check: 'pod.root-user', reason: 'A second one.' } })
  expect(second.status()).toBe(201)
  const secondId = ((await second.json()) as { id: string }).id
  // Changed behind the page's back: read again.
  await page.goto(`/#/cases/mycroft/exceptions/${first}`)
  await page.reload()
  await page.getByTestId('exception-drawer').getByTestId('exception-drawer-reactivate').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('confirm')).toContainText(`${secondId} already covers`)
  await page.getByTestId('confirm').getByRole('button', { name: 'Cancel' }).click()
  expect((await page.request.delete(`/api/v1/cases/exceptions/${secondId}`, { headers: SAME })).status()).toBeLessThan(300)

  // The object is suggested as it is typed: an object of the cluster
  // (Kubernetes' own ClusterRoleBinding, in the simulator and in kind),
  // shown with its kind, picked from the list, is the exception's object.
  await page.goto('/#/cases/mycroft/exceptions')
  await expect(page.getByTestId('knowledge-exceptions')).toBeVisible({ timeout: 20_000 })
  await page.getByTestId('exceptions-new').click()
  const pickForm = page.getByTestId('exception-new')
  await pickForm.getByTestId('exception-new-cluster').selectOption(where!.id)
  const field = pickForm.getByTestId('exception-new-object')
  await field.fill('cluster-adm')
  const suggested = pickForm.getByTestId('exception-new-objects')
  const option = suggested.getByTestId('exception-new-object-option').filter({ has: page.getByText('ClusterRoleBinding/cluster-admin', { exact: true }) })
  await expect(option).toBeVisible({ timeout: 10_000 })
  await expect(option).toContainText('ClusterRoleBinding')
  await option.click()
  await expect(field).toHaveValue('ClusterRoleBinding/cluster-admin')
  await expect(suggested).toHaveCount(0)
  await pickForm.getByTestId('exception-new-rule').fill(`other.e2e-pick-${stamp}`)
  await pickForm.getByTestId('exception-new-reason').fill('Picked from the suggestions.')
  await pickForm.getByTestId('exception-new-save').click()
  await expect(pickForm).toHaveCount(0)
  await expect(page).toHaveURL(/#\/cases\/mycroft\/exceptions\/EX-\d+$/)
  const picked = decodeURIComponent(page.url().split('/').pop()!)
  await expect(page.getByTestId('exception-drawer').getByTestId('exception-details')).toContainText('ClusterRoleBinding/cluster-admin')
  expect((await page.request.delete(`/api/v1/cases/exceptions/${picked}`, { headers: SAME })).status()).toBeLessThan(300)
})
