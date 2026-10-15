// Sherlock (Phase 5): the panel, questions answered from the cluster as
// the person asking may read it, nothing secret sent to the Claude API,
// spending caps, and the panel in a window of its own. The console talks
// to a fake Claude API here: it calls the tools a real model would and
// records every request, so what would have been sent can be checked.
import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, inKind, PLANTED, PLANTED_HELM, podRows, snapshot } from './cluster'
import { E2E_DEV, signInAs } from './accounts'
import type { PodRow, ReleaseRow } from '../src/api/types'

test.describe.configure({ mode: 'serial' })

const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
const LOG_PLANTED = ['LogPlanted-9f3K2xQ', 'LogPlantedKey-77QzXwv4']

async function put(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(
    async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(b) })).status,
    [path, body, method] as const,
  )
}

async function open(page: Page, hash = '#/nodes') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

const dock = (page: Page) => page.getByTestId('sherlock-dock')
const lastAnswer = (page: Page) => page.getByTestId('sherlock-answer').last()

async function ask(page: Page, q: string) {
  await page.getByTestId('sherlock-input').fill(q)
  await page.getByTestId('sherlock-input').press('Enter')
  await expect(page.getByTestId('sherlock-question').last()).toHaveText(q)
}

async function done(page: Page) {
  await expect(lastAnswer(page)).toHaveAttribute('data-status', /done|stopped|failed/, { timeout: 20_000 })
}

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await open(page)
  expect(await put(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })).toBe(200)
  expect(await put(page, '/api/v1/sherlock/key', null, 'DELETE')).toBe(200)
  await page.evaluate(() => localStorage.removeItem('221b.dock'))
  await page.close()
})

test.afterAll(async ({ browser }) => {
  // Off again, so the other specs see the console without the panel.
  const page = await browser.newPage()
  await open(page)
  await put(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await page.close()
})

test('Sherlock stays away until an administrator turns it on, and the key is never shown again (P5-05)', async ({ page }) => {
  await open(page, '#/alerts')
  await expect(page.getByTestId('alert').first()).toBeVisible()
  await expect(dock(page)).toHaveCount(0)
  await expect(page.getByTestId('ask-sherlock')).toHaveCount(0)

  await page.getByRole('button', { name: 'Settings' }).click()
  const settings = page.getByTestId('sherlock-settings')
  await expect(settings.getByTestId('sherlock-key-state')).toContainText('No key yet')
  await settings.getByTestId('sherlock-key').fill(KEY)
  await settings.getByTestId('sherlock-key-save').click()
  await expect(settings.getByTestId('sherlock-key-state')).toContainText(/Saved by .+\. Kept in/)
  await expect(settings.getByTestId('sherlock-key')).toHaveValue('')
  // The key never comes back, from any endpoint.
  const cfg = await page.evaluate(async () => (await fetch('/api/v1/sherlock/config')).text())
  expect(cfg).not.toContain(KEY)
  expect(cfg).toContain('"set":true')

  await settings.locator('[data-testid=sherlock-cluster][data-cluster=local]').check()
  await settings.getByTestId('sherlock-save').click()
  await expect(settings.getByText('Saved.')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dock(page)).toBeVisible()
  await expect(page.getByTestId('ask-sherlock').first()).toBeVisible()
  await expect
    .poll(async () => (await cluster.auditLog()).filter((r) => r.action === 'sherlock.key' || r.action === 'sherlock.settings').length, { timeout: 15_000 })
    .toBeGreaterThanOrEqual(2)
  expect(JSON.stringify(await cluster.auditLog())).not.toContain(KEY)
})

test('a question streams from the tools, cites what it read, and every cited link opens its object (P5-04)', async ({ page }) => {
  await open(page)
  await expect(dock(page)).toBeVisible()
  if (await page.getByTestId('sherlock-new').count()) await page.getByTestId('sherlock-new').click()
  const start = Date.now()
  await page.getByRole('button', { name: 'What needs attention right now?' }).click()
  await expect(lastAnswer(page).getByTestId('sherlock-steps')).toContainText('Read the alerts', { timeout: 5_000 })
  expect(Date.now() - start).toBeLessThan(5_000)
  await done(page)
  await expect(lastAnswer(page)).toHaveAttribute('data-status', 'done')
  await expect(lastAnswer(page).getByTestId('sherlock-text')).toContainText(/There are \d+ alerts/)
  await expect(lastAnswer(page).getByTestId('sherlock-meta')).toContainText('claude-opus-5-5')

  // Questions to ask next, as chips; the line they came in is gone.
  await expect(lastAnswer(page).getByTestId('sherlock-follow')).toContainText('Which pods use the most memory?')
  await expect(lastAnswer(page).getByTestId('sherlock-text')).not.toContainText('Ask next:')
  await expect(lastAnswer(page).getByTestId('sherlock-refs').locator('a').first()).toContainText(/^Open (node|pod) /)

  // Something no tool showed is not a link.
  await expect(lastAnswer(page).getByTestId('sherlock-text')).toContainText('Nothing is wrong with ghost.')
  await expect(lastAnswer(page).locator('a[data-cite*="ghost"]')).toHaveCount(0)

  // Every cited object opens over the tab.
  const links = lastAnswer(page).locator('a[data-cite]')
  const n = await links.count()
  expect(n).toBeGreaterThan(0)
  for (let i = 0; i < Math.min(n, 4); i++) {
    const cite = (await links.nth(i).getAttribute('data-cite'))!
    await links.nth(i).click()
    const panel = cite.includes('/Node/') ? page.getByTestId('node-panel') : page.getByTestId('pod-panel')
    await expect(panel, cite).toBeVisible()
    await expect(page).toHaveURL(/peek=/)
    await page.keyboard.press('Escape')
    await expect(panel).toHaveCount(0)
  }

  // The question is audited with its tools and tokens.
  await expect
    .poll(async () => (await cluster.auditLog()).some((r) => r.action === 'sherlock.ask' && String(r.detail ?? '').includes('tools [list_alerts]') && String(r.detail ?? '').includes('tokens in')), { timeout: 15_000 })
    .toBe(true)
})

test('the panel resizes, maximizes over the page and minimizes to an icon (P5-01)', async ({ page }) => {
  await open(page)
  const box = async () => (await dock(page).boundingBox())!.height
  const h0 = await box()
  await page.getByTestId('sherlock-grip').focus()
  await page.keyboard.press('ArrowUp')
  await expect.poll(box).toBeGreaterThan(h0 + 20)
  // Dragging the grip.
  const grip = (await page.getByTestId('sherlock-grip').boundingBox())!
  await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
  await page.mouse.down()
  await page.mouse.move(grip.x + grip.width / 2, grip.y - 80, { steps: 4 })
  await page.mouse.up()
  await expect.poll(box).toBeGreaterThan(h0 + 80)

  await page.getByTestId('sherlock-maximize').click()
  await expect(dock(page)).toHaveAttribute('data-mode', 'max')
  await expect(dock(page)).toHaveAttribute('role', 'dialog')
  await page.keyboard.press('Escape')
  await expect(dock(page)).toHaveAttribute('data-mode', 'open')

  await page.getByTestId('sherlock-minimize').click()
  await expect(dock(page)).toHaveCount(0)
  await expect(page.getByTestId('sherlock-fab')).toBeVisible()
  // Kept for this browser.
  await page.reload()
  await expect(page.getByTestId('sherlock-fab')).toBeVisible()
  await page.getByTestId('sherlock-fab').click()
  await expect(dock(page)).toBeVisible()
})

test('Ask Sherlock on a card or in a drawer asks about what it shows (P5-01)', async ({ page }) => {
  await open(page, '#/alerts')
  const button = page.getByTestId('alert').getByTestId('ask-sherlock').first()
  const q = (await button.getAttribute('data-question'))!
  await button.click()
  await expect(page.getByTestId('sherlock-question').last()).toHaveText(q)
  await done(page)

  // From a drawer the answer shows above it.
  const pod = podRows(await snapshot(request(page), ['pods'])).find((p) => p.status === 'Running')!
  await page.goto(`/#/pods?peek=${encodeURIComponent(`pod:${pod.namespace}/${pod.name}`)}`)
  await page.getByTestId('pod-panel').getByTestId('ask-sherlock').click()
  await expect(dock(page)).toHaveAttribute('data-mode', 'max')
  await expect(page.getByTestId('sherlock-question').last()).toContainText(`${pod.namespace}/${pod.name}`)
  await done(page)
  await page.keyboard.press('Escape')
  await expect(dock(page)).toHaveAttribute('data-mode', 'open')
})

function request(page: Page) {
  return page.context().request
}

test('an answer can be stopped, keeps what arrived, and the next question is answered (P5-04)', async ({ page }) => {
  await open(page)
  await ask(page, 'Answer slowly please')
  await expect(lastAnswer(page).getByTestId('sherlock-text')).toContainText('Part 2', { timeout: 10_000 })
  await page.getByTestId('sherlock-stop').click()
  await expect(lastAnswer(page)).toHaveAttribute('data-status', 'stopped')
  await expect(lastAnswer(page).getByTestId('sherlock-text')).not.toContainText('Part 50')
  await expect(lastAnswer(page).getByTestId('sherlock-stopped')).toBeVisible()
  await ask(page, 'What needs attention right now?')
  await done(page)
  await expect(lastAnswer(page)).toHaveAttribute('data-status', 'done')
  await page.getByTestId('sherlock-new').click()
  await expect(page.getByTestId('sherlock-empty')).toBeVisible()
})

test('Secret values and credentials in logs never reach the Claude API (P5-03)', async ({ page }) => {
  const planted = await cluster.plantedSecret()
  try {
    await open(page)
    await cluster.resetClaude()
    const snap = await snapshot(request(page), ['pods', 'helm'])
    const running = podRows(snap).filter((p: PodRow) => p.status === 'Running' && p.namespace !== 'kube-system')
    const releases = (snap.views.helm?.rows ?? []) as ReleaseRow[]
    await ask(page, `Tell me about secret ${planted.namespace}/${planted.name}`)
    await done(page)
    for (const p of running.slice(0, 2)) {
      await ask(page, `show me the logs of ${p.namespace}/${p.name}`)
      await done(page)
    }
    for (const r of releases.slice(0, 3)) {
      await ask(page, `Tell me about Helm release ${r.namespace}/${r.name}`)
      await done(page)
    }
    const sent = await cluster.claudeRequests()
    expect(sent.length).toBeGreaterThan(3)
    const all = sent.join('\n')
    for (const v of [PLANTED, PLANTED_HELM, KEY, ...LOG_PLANTED]) expect(all, `${v} reached the Claude API`).not.toContain(v)
    // The Secret was read: by key name and size only.
    expect(all).toContain(planted.name)
    if (!inKind) expect(all, 'the planted log lines were scrubbed, not skipped').toContain('[REDACTED]@db.internal')
  } finally {
    await planted.cleanup()
  }
})

test('a person without access to a namespace learns nothing about it through Sherlock (P5-02)', async ({ page, browser }) => {
  const me = (await (await request(page).get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const allowed = await cluster.limitDev()
  const all = podRows(await snapshot(request(page), ['pods']))
  const hidden = all.filter((p) => !allowed.includes(p.namespace))
  const target = hidden[0]!
  const others = hidden.filter((p) => p.namespace === target.namespace && p.name !== target.name)
  const dev = await signInAs(browser, E2E_DEV)
  try {
    await dev.goto('/#/pods')
    await expect(dev.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
    await expect(dev.getByTestId('sherlock-dock')).toBeVisible()
    await cluster.resetClaude()
    for (const q of [`Tell me about pod ${target.namespace}/${target.name}`, `Which pods are in namespace ${target.namespace}?`, `show me the logs of ${target.namespace}/${target.name}`, 'What needs attention right now?']) {
      await ask(dev, q)
      await done(dev)
    }
    const sent = (await cluster.claudeRequests()).join('\n')
    expect(sent.length).toBeGreaterThan(0)
    expect(sent, 'the hidden pod’s UID').not.toContain(target.uid)
    for (const p of others.slice(0, 20)) expect(sent, `${p.key} reached the Claude API`).not.toContain(p.name)
    await expect(dev.getByTestId('sherlock-answer').nth(1)).toContainText(/could not see any pods|None/)
  } finally {
    await dev.context().close()
  }
})

test('a spending cap stops new questions with a clear message (P5-05)', async ({ page }) => {
  await open(page)
  expect(await put(page, '/api/v1/sherlock/config', { ...ON, dailyUSD: 0.0001 })).toBe(200)
  try {
    await expect(page.getByTestId('sherlock-unavailable')).toContainText('daily spending cap', { timeout: 15_000 })
    const status = await page.evaluate(async () => (await fetch('/api/v1/sherlock/ask', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"question":"Anything?"}' })).status)
    expect(status).toBe(429)
    await expect(page.getByTestId('ask-sherlock')).toHaveCount(0)
  } finally {
    expect(await put(page, '/api/v1/sherlock/config', ON)).toBe(200)
  }
  await expect(page.getByTestId('sherlock-input')).toBeVisible({ timeout: 15_000 })
})

test('Sherlock moves to a window of its own and back, and the main window asks there (P5-08)', async ({ page, context }) => {
  await open(page, '#/alerts')
  if (await page.getByTestId('sherlock-new').count()) await page.getByTestId('sherlock-new').click()
  const [popup] = await Promise.all([context.waitForEvent('page'), page.getByTestId('sherlock-popout').click()])
  await popup.waitForLoadState('domcontentloaded')
  await expect(popup).toHaveURL(/#\/sherlock$/)
  await expect(popup.getByTestId('sherlock')).toBeVisible()
  await expect(page.getByTestId('sherlock-popped')).toBeVisible()
  await expect(dock(page)).toHaveCount(0)

  // Asked in the main window, answered in the other.
  const button = page.getByTestId('alert').getByTestId('ask-sherlock').first()
  const q = (await button.getAttribute('data-question'))!
  await button.click()
  await expect(popup.getByTestId('sherlock-question').last()).toHaveText(q)
  await expect(popup.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', 'done', { timeout: 20_000 })

  // A citation there opens in the main window.
  await popup.locator('a[data-cite]').first().click()
  await expect(page).toHaveURL(/peek=/)
  await page.keyboard.press('Escape')

  // Docking it back closes the window, and the conversation is here.
  await popup.getByTestId('sherlock-dock-back').click()
  await expect.poll(() => popup.isClosed()).toBe(true)
  await expect(dock(page)).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('sherlock-question').last()).toHaveText(q)
})

test('the panel meets WCAG 2.2 AA, docked, maximized and in the settings (X-12)', async ({ page }) => {
  const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']
  const violations = async () => {
    await page.evaluate(() => Promise.allSettled(document.getAnimations().map((a) => a.finished)))
    const r = await new AxeBuilder({ page }).include('[data-testid=sherlock-dock], [data-testid=sherlock-settings], [data-testid=sherlock-fab]').withTags(TAGS).analyze()
    return r.violations.map((v) => `${v.id}: ${v.help} — ${v.nodes.slice(0, 3).map((n) => n.target.join(' ')).join(', ')}`)
  }
  for (const theme of ['light', 'dark'] as const) {
    await page.addInitScript((t) => window.localStorage.setItem('221b.theme', JSON.stringify(t)), theme)
    await open(page)
    await ask(page, 'Tell me about pod ' + podRows(await snapshot(request(page), ['pods']))[0]!.key)
    await done(page)
    expect(await violations(), theme).toEqual([])
    await page.getByTestId('sherlock-maximize').click()
    expect(await violations(), theme + ' maximized').toEqual([])
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(page.getByTestId('sherlock-settings').getByTestId('sherlock-save')).toBeVisible()
    expect(await violations(), theme + ' settings').toEqual([])
    await page.keyboard.press('Escape')
  }
})
