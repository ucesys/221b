// Mycroft's knowledge (Phase 12, KN-01 to KN-18): Settings › Knowledge
// lists the sources and keeps the wiki in the temporary ConfigMap store
// until another is chosen (CF-57), saying it is temporary;
// a source's token goes in and never comes back from any endpoint; the
// webhook refuses a call without its token; and Settings › Issues has the
// Confluence publisher's part. AIOps › Mycroft (Phase 12, wave 5, MY-01
// to MY-10): its sub-tabs and addresses, the Wiki in the default store
// (never "Loading…" for good), and, since MY-11, adding in Settings ›
// Knowledge's Added in Mycroft card with a store (the simulator's fake S3
// bucket): a note, a CSV document, and an address the console does not
// read. A source's Test is in its dialog, with the values as typed (KN-26). A page's own actions (KN-23, KN-24): This is outdated…
// sends a correction, and a page retired by one person and approved by
// another leaves the agents' index, then comes back the same way.
import { createHash, createHmac } from 'node:crypto'
import { E2E_ADMIN2, signInAs } from './accounts'
import { expect, test } from './test'
import { cluster, inKind } from './cluster'

const TOKEN = 'e2e-confluence-read-token-7f3a'

test('Settings › Knowledge takes a source whose token never comes back, and keeps the wiki in the temporary ConfigMap store by default', async ({ page }) => {
  await page.goto('/#/admin/settings/knowledge')
  const section = page.getByTestId('knowledge-section')
  await expect(section).toBeVisible({ timeout: 20_000 })
  await expect(section.locator('[data-testid=knowledge-source][data-source=console]')).toBeVisible()
  await expect(section.locator('[data-testid=knowledge-source][data-source=documents]')).toBeVisible()
  // The fixes proven in cases, there from the start, offered by the
  // housekeeping for a runbook (KN-34).
  await expect(section.locator('[data-testid=knowledge-source][data-source=fixes]')).toContainText('offered by the housekeeping for a runbook')
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

test('AIOps › Mycroft has Wiki, Known issues and Exceptions (no RCAs: an RCA is part of its case); the Wiki is kept in the default ConfigMap store', async ({ page }) => {
  await page.goto('/#/aiops/mycroft')
  const tab = page.getByTestId('mycroft-tab')
  await expect(tab).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('mycroft-sub-wiki')).toHaveAttribute('aria-pressed', 'true')
  // The bug of the deployed console: "Loading…" for good. The wiki is
  // kept in the temporary ConfigMap store by default (CF-57): no banner
  // asks for a store, and Add is open.
  await expect(page.getByTestId('knowledge-pages')).not.toContainText('Loading…', { timeout: 15_000 })
  await expect(page.getByTestId('wiki-no-store')).toHaveCount(0)
  // Add is in Settings › Knowledge only (MY-11): the Wiki has no Add view.
  await expect(page.getByTestId('knowledge-view-add')).toHaveCount(0)
  await page.getByTestId('knowledge-view-sources').click()
  await expect(page.getByTestId('wiki-sources').locator('[data-testid=wiki-source][data-source=added]')).toContainText('Added in Mycroft')

  for (const [sub, url, view] of [
    ['known', /#\/aiops\/mycroft\/known$/, 'known-tab'],
    ['exceptions', /#\/aiops\/mycroft\/exceptions$/, 'knowledge-exceptions'],
    ['wiki', /#\/aiops\/mycroft\/wiki$/, 'knowledge-tab'],
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
  // The Add view's old address opens Settings › Knowledge's Added in Mycroft card (MY-11).
  await page.goto('/#/cases/mycroft/wiki/add')
  await expect(page.getByTestId('knowledge-added').getByTestId('wiki-add')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('knowledge-section')).toBeVisible()
  await expect(page.getByTestId('wiki-add-refused')).toHaveCount(0)
})

test('Settings › Knowledge › Added in Mycroft with a store: a note and a CSV become sources the wiki quotes; a loopback address is refused', async ({ page }) => {
  test.skip(inKind, 'the fake S3 bucket is the simulator’s')
  test.setTimeout(120_000)
  await page.goto('/#/admin/settings/knowledge/added')
  const card = page.getByTestId('knowledge-added')
  await expect(card.getByTestId('wiki-add')).toBeVisible({ timeout: 20_000 })
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
    await expect(card.getByTestId('wiki-add')).toBeVisible({ timeout: 20_000 })
    await expect(card.getByTestId('wiki-add-refused')).toHaveCount(0, { timeout: 15_000 })

    // A note, with a password that never reaches the wiki.
    await card.getByTestId('wiki-add-title').fill('E2E zephyr backups')
    await card.getByTestId('wiki-add-text').fill('The zephyr backups run nightly on nas-7 and are kept for 30 days.\n\npassword=e2e-hunter2-secret-value')
    await card.getByTestId('wiki-add-submit').click()
    await expect(card.getByTestId('wiki-add-result')).toContainText('“E2E zephyr backups” was added.')
    await expect(card.getByTestId('wiki-added-item').filter({ hasText: 'E2E zephyr backups' })).toBeVisible()

    // A CSV document.
    await card.getByTestId('wiki-add-kind-document').click()
    await card.getByTestId('wiki-add-file').setInputFiles({ name: 'e2e-quokka-hosts.csv', mimeType: 'text/csv', buffer: Buffer.from('host,address,role\nquokka-1,10.9.0.1,cache\n') })
    await expect(card.getByTestId('wiki-add-files')).toContainText('e2e-quokka-hosts.csv')
    await card.getByTestId('wiki-add-submit').click()
    await expect(card.getByTestId('wiki-add-result')).toContainText('“e2e-quokka-hosts.csv” was added.')

    // An address inside the console's own pod is not read.
    await card.getByTestId('wiki-add-kind-url').click()
    await card.getByTestId('wiki-add-url').fill('http://127.0.0.1:8080/')
    await card.getByTestId('wiki-add-submit').click()
    await expect(card.getByTestId('wiki-add-error')).toContainText('does not read loopback')

    // Both are read, listed under Sources, and quoted by the search.
    await expect
      .poll(
        async () => {
          const w = JSON.parse((await api('/api/v1/knowledge/wiki')).body) as { added: { title: string; lastRead?: string }[] }
          return w.added.filter((a) => a.lastRead && /zephyr|quokka/.test(a.title)).length
        },
        { timeout: 60_000 },
      )
      .toBe(2)
    await page.goto('/#/aiops/mycroft/wiki/sources')
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

// --- A wiki page's own actions (KN-23, KN-24) ---

/**
 * Puts an object in the simulator's fake bucket as a person would, outside
 * the console: a PUT signed with AWS Signature Version 4 (the fake checks
 * it), with the bucket's test key (MY-10).
 */
async function putSimObject(key: string, body: string): Promise<void> {
  const url = new URL(`${SIM}/sim-wiki/${key}`)
  const sha = (s: string) => createHash('sha256').update(s).digest('hex')
  const hmac = (k: string | Buffer, s: string) => createHmac('sha256', k).update(s).digest()
  const amzDate = new Date()
    .toISOString()
    .replace(/[-:]/g, '')
    .replace(/\.\d{3}/, '')
  const day = amzDate.slice(0, 8)
  const payload = sha(body)
  const headers: Record<string, string> = { 'content-type': 'text/markdown', host: url.host, 'x-amz-content-sha256': payload, 'x-amz-date': amzDate }
  const names = Object.keys(headers).sort()
  const canonical = ['PUT', url.pathname, '', names.map((n) => `${n}:${headers[n]}\n`).join(''), names.join(';'), payload].join('\n')
  const scope = `${day}/us-east-1/s3/aws4_request`
  const toSign = `AWS4-HMAC-SHA256\n${amzDate}\n${scope}\n${sha(canonical)}`
  let k = hmac('AWS4sim-secret-key', day)
  for (const part of ['us-east-1', 's3', 'aws4_request']) k = hmac(k, part)
  const sig = createHmac('sha256', k).update(toSign).digest('hex')
  // Host is signed but set by fetch itself, from the address.
  const sent = Object.fromEntries(Object.entries(headers).filter(([n]) => n !== 'host'))
  const r = await fetch(url, { method: 'PUT', body, headers: { ...sent, authorization: `AWS4-HMAC-SHA256 Credential=sim-access-key/${scope}, SignedHeaders=${names.join(';')}, Signature=${sig}` } })
  expect(r.status, await r.text()).toBe(200)
}

test('a wiki page: This is outdated… sends a correction; Retire page asked by one person and approved by another takes it out of the index, and Make active again brings it back', async ({ page, browser }) => {
  test.skip(inKind, 'the fake S3 bucket is the simulator’s')
  test.setTimeout(150_000)
  const stamp = Date.now().toString(36)
  const prefix = `e2e-retire-${stamp}/`
  const path = `pages/system/e2e-wombat-${stamp}.md`
  const word = `wombat${stamp}`
  // An approved page, written straight into the bucket as a person would.
  await putSimObject(prefix + path, `---\ntitle: E2E ${word} backups\nkind: system\n---\n\n# E2E ${word} backups\n\nThe ${word} backups are kept 30 days on nas-1.\n`)
  await page.goto('/#/aiops/mycroft/wiki')
  await expect(page.getByTestId('knowledge-tab')).toBeVisible({ timeout: 20_000 })
  const api = (p: string, method = 'GET', body?: unknown, on = page) =>
    on.evaluate(
      async ([u, m, b]) => {
        const r = await fetch(u as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) })
        return { status: r.status, body: await r.text() }
      },
      [p, method, body] as const,
    )
  const store = { kind: 's3', endpoint: SIM, region: 'us-east-1', bucket: 'sim-wiki', prefix, pathStyle: true, credentials: 'keys' }
  const saved = await api('/api/v1/knowledge/store', 'PUT', { store, keys: { accessKeyId: 'sim-access-key', secretAccessKey: 'sim-secret-key' } })
  expect(saved.status, saved.body).toBe(200)
  const other = await signInAs(browser, E2E_ADMIN2)
  try {
    await page.goto('/#/aiops/mycroft/wiki/' + path)
    const view = page.getByTestId('knowledge-page')
    await expect(view).toContainText(`kept 30 days on nas-1`, { timeout: 15_000 })
    expect((await api(`/api/v1/knowledge/search?q=${word}`)).body).toContain(path)

    // This is outdated…: a correction of the page, kept as a note tied to it.
    await view.getByTestId('wiki-page-outdated').click()
    await page.getByTestId('wiki-outdated-text').fill(`The ${word} backups are kept 90 days now, on nas-2.`)
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByTestId('confirm')).toHaveCount(0)
    await expect
      .poll(
        async () => {
          const w = JSON.parse((await api('/api/v1/knowledge/wiki')).body) as { added: { title: string; corrects?: string; by?: string }[] }
          return w.added.some((a) => a.corrects === path && a.title === `Correction to E2E ${word} backups`)
        },
        { timeout: 30_000 },
      )
      .toBe(true)

    // Retire page: asked here, refused to the one who asked (403), approved by a second person.
    await view.getByTestId('wiki-page-retire').click()
    await page.getByTestId('wiki-request-reason').fill('The NAS was replaced.')
    await page.getByTestId('confirm-ok').click()
    await expect(view.getByTestId('wiki-page-request')).toContainText('to retire this page: “The NAS was replaced.”')
    const asked = JSON.parse((await api('/api/v1/knowledge/requests')).body) as { requests: { id: string; path: string; mine: boolean }[] }
    const req = asked.requests.find((r) => r.path === path)!
    expect(req.mine).toBe(true)
    expect((await api(`/api/v1/knowledge/requests/${req.id}/approve`, 'POST', {})).status).toBe(403)
    await other.goto(`/#/aiops/inbox/knowledge/${req.id}`)
    const card = other.getByTestId('page-request')
    await expect(card).toContainText('The NAS was replaced.', { timeout: 20_000 })
    await card.getByTestId('page-request-approve').click()
    await expect(card.getByTestId('page-request-state')).toHaveText('Approved')

    // Retired: shown with why and who, under Retired; no longer found for the agents.
    await page.reload()
    await expect(view.getByTestId('wiki-page-retired')).toContainText('The NAS was replaced.', { timeout: 15_000 })
    await expect(view.getByTestId('wiki-page-retired')).toContainText('approved by')
    await page.getByTestId('knowledge-pages-retired').click()
    await expect(page.getByTestId('knowledge-retired')).toContainText(`E2E ${word} backups`)
    expect((await api(`/api/v1/knowledge/search?q=${word}&kind=page`)).body).not.toContain(path)
    await expect(view.getByTestId('wiki-page-outdated')).toHaveCount(0)

    // Make active again, the same way.
    await view.getByTestId('wiki-page-activate').click()
    await page.getByTestId('wiki-request-reason').fill('The NAS is back.')
    await page.getByTestId('confirm-ok').click()
    await expect(view.getByTestId('wiki-page-request')).toContainText('to make this page active again')
    const again = (JSON.parse((await api('/api/v1/knowledge/requests')).body) as { requests: { id: string; path: string }[] }).requests.find((r) => r.path === path)!
    const approved = await api(`/api/v1/knowledge/requests/${again.id}/approve`, 'POST', {}, other)
    expect(approved.status, approved.body).toBe(200)
    await page.reload()
    await expect(view).toContainText('kept 30 days on nas-1', { timeout: 15_000 })
    await expect(view.getByTestId('wiki-page-retired')).toHaveCount(0)
    expect((await api(`/api/v1/knowledge/search?q=${word}&kind=page`)).body).toContain(path)

    await expect
      .poll(
        async () => {
          const actions = (await cluster.auditLog()).map((r) => r.action)
          return ['knowledge.outdated', 'knowledge.retire.request', 'knowledge.retire', 'knowledge.activate'].every((a) => actions.includes(a))
        },
        { timeout: 15_000 },
      )
      .toBe(true)
    expect(JSON.stringify(await cluster.auditLog())).not.toContain('kept 90 days now')
  } finally {
    await other.context().close()
    const reqs = JSON.parse((await api('/api/v1/knowledge/requests')).body) as { requests?: { id: string }[] }
    for (const r of reqs.requests ?? []) await api(`/api/v1/knowledge/requests/${r.id}/reject`, 'POST', { comment: 'e2e' })
    const drafts = JSON.parse((await api('/api/v1/knowledge/drafts')).body) as { runs?: { id: string }[] }
    for (const r of drafts.runs ?? []) await api(`/api/v1/knowledge/drafts/${r.id}/reject`, 'POST', { comment: 'e2e' })
    const back = await api('/api/v1/knowledge/store', 'PUT', { store: { kind: '' }, keys: {} })
    expect(back.status, back.body).toBe(200)
  }
})

// A bucket of documents (Phase 12, KN-25): the simulator's second fake
// bucket, sim-docs (MY-10), holds a Markdown runbook and a CSV under docs/
// and a picture Mycroft skips. Added in Settings › Knowledge with a
// read-only access key that never comes back, and tested. Its Wiki sync
// job (KN-28) is in AIOps › Jobs › Schedule, nightly, and read the bucket
// at once as a run of it: the fake model cut its answer to the batch of
// both documents (the runbook carries its mark), so the batch was split
// and each asked again (KN-27). Run now runs the job again. Settings
// starts no sync; its items are listed under Mycroft › Wiki › Sources.
// Removing the source removes its job.
test('Settings › Knowledge reads an S3 bucket of documents through its Wiki sync job in AIOps › Jobs', async ({ page }) => {
  test.skip(inKind, 'the fake S3 bucket is the simulator’s')
  test.setTimeout(180_000)
  const SECRET = 'sim-secret-key'
  const ID = 'e2e-docs-bucket'
  const JOB = `wiki-sync-${ID}`
  const api = (path: string, method = 'GET', body?: unknown) =>
    page.evaluate(
      async ([p, m, b]) => {
        const r = await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) })
        return { status: r.status, body: await r.text() }
      },
      [path, method, body] as const,
    )
  type Brief = { id: string; status: string; trigger: { what: string } }
  const runs = async () => (JSON.parse((await api(`/api/v1/jobs/${JOB}/runs`)).body) as { runs?: Brief[] }).runs ?? []
  // Mycroft writes the pages with the fake model, as the other specs give it.
  const SHERLOCK = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
  await page.goto('/#/nodes')
  expect((await api('/api/v1/sherlock/key', 'PUT', { key: 'sk-ant-e2e-fake-key-0123456789' })).status).toBe(200)
  expect((await api('/api/v1/sherlock/config', 'PUT', SHERLOCK)).status).toBe(200)
  await page.goto('/#/admin/settings/knowledge')
  const section = page.getByTestId('knowledge-section')
  await expect(section).toBeVisible({ timeout: 20_000 })
  // No sync starts here: one line says where the syncs are.
  await expect(page.getByTestId('knowledge-sync')).toHaveCount(0)
  await expect(page.getByTestId('knowledge-jobs-line')).toHaveText('Each source is read by its Wiki sync job in AIOps › Jobs.')
  await page.getByTestId('knowledge-add').click()
  const drawer = page.getByTestId('knowledge-source-drawer')
  await drawer.getByTestId('knowledge-source-kind').selectOption('s3')
  await expect(drawer.getByTestId('knowledge-source-token')).toHaveCount(0)
  await drawer.getByTestId('knowledge-source-name').fill('E2E docs bucket')
  await drawer.getByTestId('knowledge-source-bucket').fill('sim-docs')
  await drawer.getByTestId('knowledge-source-prefix').fill('docs/')
  await drawer.getByTestId('knowledge-source-region').fill('us-east-1')
  await drawer.getByTestId('knowledge-source-endpoint').fill(SIM)
  await drawer.getByTestId('knowledge-source-pathstyle').check()
  await drawer.getByTestId('knowledge-source-akid').fill('sim-access-key')
  await drawer.getByTestId('knowledge-source-secret').fill(SECRET)

  // Test in the dialog, as typed, before saving (KN-26): the first keys and
  // what is skipped, kept until a field changes; a port where nothing
  // listens is said plainly.
  const result = drawer.getByTestId('knowledge-source-test-result')
  const reached = 'Reached: 2 items, e.g. docs/backup-windows.csv, docs/runbooks/restart-the-wombat-cache.md. 1 skipped: 1 not a kind Mycroft reads (.png).'
  await drawer.getByTestId('knowledge-source-test').click()
  await expect(result).toHaveText(reached, { timeout: 35_000 })
  await expect(result).toHaveAttribute('data-state', 'ok')
  await drawer.getByTestId('knowledge-source-endpoint').fill('http://127.0.0.1:1')
  await expect(result).toHaveCount(0)
  await drawer.getByTestId('knowledge-source-test').click()
  await expect(result).toContainText('Could not connect to 127.0.0.1:1', { timeout: 35_000 })
  await expect(result).toHaveAttribute('data-state', 'error')
  await drawer.getByTestId('knowledge-source-endpoint').fill(SIM)
  expect((await api('/api/v1/knowledge/settings')).body).not.toContain(ID)
  await drawer.getByTestId('knowledge-source-save').click()
  const row = section.locator(`[data-testid=knowledge-source][data-source=${ID}]`)
  try {
    await expect(row.getByTestId('knowledge-source-creds')).toHaveText('access key in a Secret')
    await expect(row.getByTestId('knowledge-source-bucket-line')).toContainText('s3://sim-docs/docs/')
    await expect(row.getByTestId('knowledge-source-job')).toHaveAttribute('href', `#/aiops/jobs/${JOB}`)
    for (const path of ['/api/v1/knowledge/settings', '/api/v1/knowledge/status', '/api/v1/knowledge/wiki', `/api/v1/jobs/${JOB}`]) {
      expect((await api(path)).body).not.toContain(SECRET)
    }

    // Edit…: Test with the secret left blank uses the saved one, which never comes back.
    await expect(row.getByTestId('knowledge-source-test')).toHaveCount(0)
    await row.getByText('Edit…').click()
    await expect(drawer.getByTestId('knowledge-source-secret')).toHaveValue('')
    await drawer.getByTestId('knowledge-source-test').click()
    await expect(result).toHaveText(reached, { timeout: 35_000 })
    await drawer.getByRole('button', { name: 'Cancel' }).click()
    await expect(drawer).toHaveCount(0)

    // Added, it was read at once, as a run of its job.
    await expect.poll(async () => ((await runs()).length && (await runs()).every((r) => r.status !== 'running') ? (await runs())[0]!.status : ''), { timeout: 90_000 }).toBe('ok')
    const first = (await runs()).at(-1)!
    expect(first.trigger.what).toMatch(/^Source added by /)

    // Its job, in AIOps › Jobs › Schedule: Mycroft's, every night.
    await page.goto('/#/aiops/jobs')
    const job = page.locator(`[data-testid=job-row][data-job=${JOB}]`)
    await expect(job).toContainText('Wiki sync: E2E docs bucket', { timeout: 20_000 })
    // Its agent beside its name (I-5).
    await expect(job.getByTestId('job-agent')).toContainText('Mycroft')
    await expect(job).toContainText('Every day at 01:00')

    // The first read's run: what it read, the split batch, the drafts linked, no findings.
    await page.goto(`/#/aiops/jobs/runs/${first.id}`)
    const detail = page.getByTestId('run-detail')
    const fact = (label: string) => detail.locator(`[data-testid=run-fact][data-label="${label}"]`)
    await expect(fact('Items read')).toHaveText('2 changed items', { timeout: 20_000 })
    await expect(fact('Skipped')).toContainText('1 item: 1 not a kind Mycroft reads (.png)')
    await expect(fact('Source')).toHaveText('E2E docs bucket (S3 bucket)')
    await expect(fact('Pages drafted').locator('a')).toHaveAttribute('href', /^#\/aiops\/inbox\/knowledge\/r-/)
    await expect(fact('Mycroft’s batches')).toContainText('1 cut and split in two')
    await expect(detail.getByTestId('run-report')).toContainText('split in two and asked again: its answer was cut at the most Mycroft may write')
    await expect(detail.getByTestId('run-routed')).toHaveCount(0)

    // Run now runs the job again: nothing changed since.
    await page.goto(`/#/aiops/jobs/${JOB}`)
    await page.getByTestId('job-run-now').click()
    await expect
      .poll(
        async () => {
          const r = await runs()
          return r.length >= 2 && r[0]!.status !== 'running' ? r[0]!.trigger.what : ''
        },
        { timeout: 90_000 },
      )
      .toMatch(/^Run now by /)
    await page.goto(`/#/aiops/jobs/runs/${(await runs())[0]!.id}`)
    await expect(fact('Items read')).toHaveText('0 changed items', { timeout: 20_000 })
    await expect(fact('Unchanged')).toHaveText('2 items')

    // Settings › Knowledge counts it, and still starts no sync.
    await page.goto('/#/admin/settings/knowledge')
    await expect(row.getByTestId('knowledge-source-state')).toHaveText('Read · 2 items · 1 skipped', { timeout: 15_000 })
    await expect(page.getByTestId('knowledge-sync')).toHaveCount(0)
    await expect.poll(async () => (await cluster.auditLog()).some((r) => r.action === 'knowledge.source' && String(r.target) === ID), { timeout: 15_000 }).toBe(true)
    expect(JSON.stringify(await cluster.auditLog())).not.toContain(SECRET)

    // Mycroft › Wiki › Sources lists it with its items; the search quotes it, by its s3:// address.
    await page.goto('/#/aiops/mycroft')
    await expect(page.getByTestId('mycroft-tab')).toBeVisible({ timeout: 20_000 })
    await page.getByTestId('knowledge-view-sources').click()
    const listed = page.getByTestId('wiki-sources').locator(`[data-testid=wiki-source][data-source=${ID}]`)
    await expect(listed).toContainText('S3 bucket')
    await expect(listed).toContainText('2')
    await expect(listed.getByTestId('wiki-source-skipped')).toHaveText('1 skipped')
    await page.getByTestId('knowledge-view-pages').click()
    await page.getByTestId('knowledge-search').fill('wombat cache rollout')
    await expect(page.getByTestId('knowledge-hits')).toContainText('s3://sim-docs/docs/runbooks/restart-the-wombat-cache.md')
  } finally {
    // Mycroft's drafts are rejected, and the source removed, for the other tests; its job goes with it.
    const drafts = JSON.parse((await api('/api/v1/knowledge/drafts')).body) as { runs?: { id: string }[] }
    for (const r of drafts.runs ?? []) await api(`/api/v1/knowledge/drafts/${r.id}/reject`, 'POST', { comment: 'e2e' })
    await api(`/api/v1/knowledge/sources/${ID}`, 'DELETE')
  }
  expect((await api(`/api/v1/jobs/${JOB}`)).status).toBe(404)
  await api('/api/v1/sherlock/config', 'PUT', { ...SHERLOCK, clusters: {} })
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
  await page.goto('/#/aiops/mycroft/exceptions')
  await expect(page.getByTestId('knowledge-exceptions')).toBeVisible({ timeout: 20_000 })
  const listed = (await (await page.request.get('/api/v1/aiops/exceptions')).json()) as { clusters?: { id: string; name: string }[] }
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
  await expect(page).toHaveURL(/#\/aiops\/mycroft\/exceptions\/EX-\d+$/)
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
  const again = await page.request.post('/api/v1/aiops/exceptions', { headers: SAME, data: { cluster: where!.id, resource: object, check: 'pod.root-user', reason: 'again' } })
  expect(again.status()).toBe(409)

  // Turned off: Inactive, saying by whom; then made active again.
  await drawer.getByTestId('exception-drawer-remove').click()
  await page.getByTestId('confirm-ok').click()
  await expect(drawer.getByTestId('exception-drawer-inactive')).toContainText('turned off by')
  await expect(drawer.getByTestId('exception-drawer-state')).toHaveText('Inactive')
  await drawer.getByTestId('exception-drawer-reactivate').click()
  await page.getByTestId('confirm-ok').click()
  await expect(drawer.getByTestId('exception-drawer-state')).toHaveText('Active')
  const back = (await (await page.request.get(`/api/v1/aiops/exceptions/${first}`)).json()) as { state: string; removedBy?: string }
  expect(back.state).toBe('active')
  expect(back.removedBy).toBeUndefined()

  // Turned off again, and another made for the same: the first may not come back while the second covers it.
  expect((await page.request.delete(`/api/v1/aiops/exceptions/${first}`, { headers: SAME })).status()).toBeLessThan(300)
  const second = await page.request.post('/api/v1/aiops/exceptions', { headers: SAME, data: { cluster: where!.id, resource: object, check: 'pod.root-user', reason: 'A second one.' } })
  expect(second.status()).toBe(201)
  const secondId = ((await second.json()) as { id: string }).id
  // Changed behind the page's back: read again.
  await page.goto(`/#/aiops/mycroft/exceptions/${first}`)
  await page.reload()
  await page.getByTestId('exception-drawer').getByTestId('exception-drawer-reactivate').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('confirm')).toContainText(`${secondId} already covers`)
  await page.getByTestId('confirm').getByRole('button', { name: 'Cancel' }).click()
  expect((await page.request.delete(`/api/v1/aiops/exceptions/${secondId}`, { headers: SAME })).status()).toBeLessThan(300)

  // The object is suggested as it is typed: an object of the cluster
  // (Kubernetes' own ClusterRoleBinding, in the simulator and in kind),
  // shown with its kind, picked from the list, is the exception's object.
  await page.goto('/#/aiops/mycroft/exceptions')
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
  await expect(page).toHaveURL(/#\/aiops\/mycroft\/exceptions\/EX-\d+$/)
  const picked = decodeURIComponent(page.url().split('/').pop()!)
  await expect(page.getByTestId('exception-drawer').getByTestId('exception-details')).toContainText('ClusterRoleBinding/cluster-admin')
  expect((await page.request.delete(`/api/v1/aiops/exceptions/${picked}`, { headers: SAME })).status()).toBeLessThan(300)
})
