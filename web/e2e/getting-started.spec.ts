// Getting started (Phase 12, GS-01 to GS-10): the checklist a SuperAdmin
// sees on the home page and in Settings, each step checked against the
// console as it is and ticking itself, each linking to its screen, and
// dismissed and brought back with an audit record; and Sherlock's setup
// skill answering "What's next?" from it. The console talks to the
// simulator's fake Claude API.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster } from './cluster'

test.describe.configure({ mode: 'serial' })

const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

interface Step {
  id: string
  n: number
  title: string
  state: 'done' | 'open' | 'dismissed'
  detail: string
  link: string
  where: string
}
interface View {
  steps: Step[]
  next?: string
  complete: boolean
  open: number
}

async function call(page: Page, path: string, body: unknown, method = 'PUT'): Promise<number> {
  return page.evaluate(
    async ([p, b, m]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === null ? undefined : JSON.stringify(b) })).status,
    [path, body, method] as const,
  )
}

async function checklist(page: Page): Promise<View> {
  const res = await page.request.get('/api/v1/setup')
  expect(res.status()).toBe(200)
  return (await res.json()) as View
}

const step = (page: Page, id: string) => page.locator(`[data-testid="setup-step"][data-step="${id}"]`)

// What the console had before: the key, and the steps dismissed, put back
// at the end so the other specs see it as they would have.
let keyBefore = false
let dismissedBefore: string[] = []

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await call(page, '/api/v1/sherlock/config', { ...ON, clusters: {} })
  if (!keyBefore) await call(page, '/api/v1/sherlock/key', null, 'DELETE')
  const now = await checklist(page)
  const back = now.steps.filter((s) => s.state === 'dismissed' && !dismissedBefore.includes(s.id)).map((s) => s.id)
  if (back.length) await call(page, '/api/v1/setup/dismiss', { steps: back, dismiss: false }, 'POST')
  await page.close()
})

test('the checklist is the console as it is, and a step ticks itself once done (GS-01 to GS-03, GS-05)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const before = await checklist(page)
  dismissedBefore = before.steps.filter((s) => s.state === 'dismissed').map((s) => s.id)
  keyBefore = before.steps.find((s) => s.id === 'provider')?.state === 'done'
  // No key: the provider's step is open and says so.
  const removed = keyBefore ? await call(page, '/api/v1/sherlock/key', null, 'DELETE') : 200
  await page.goto('/#/admin/settings/start')
  const nav = page.getByTestId('settings-section')
  await expect(nav.first()).toHaveAttribute('data-section', 'start')
  await expect(nav.first()).toContainText(/Getting started\s*(\d+ steps? left|Every step done or dismissed)/)
  await expect(page.getByTestId('setup-step')).toHaveCount(9)
  await expect(page.getByTestId('setup-step').first()).toContainText('Sign-in')
  // The simulator signs people in through OIDC, and its cluster is connected.
  await expect(step(page, 'signin')).toHaveAttribute('data-state', 'done')
  await expect(step(page, 'signin').getByTestId('setup-detail')).toHaveText('People sign in through OIDC.')
  await expect(step(page, 'clusters')).toHaveAttribute('data-state', 'done')
  // Fleets: done exactly when a cluster has one.
  const settings = (await (await page.request.get('/api/v1/settings')).json()) as { fleets?: Record<string, unknown[]> }
  const fleets = Object.values(settings.fleets ?? {}).some((l) => l.length > 0)
  if (!dismissedBefore.includes('fleets') || fleets) await expect(step(page, 'fleets')).toHaveAttribute('data-state', fleets ? 'done' : 'open')
  // Nothing is ticked by hand: a done step offers no control.
  await expect(step(page, 'signin').getByRole('button')).toHaveCount(0)
  if (removed === 200 && !dismissedBefore.includes('provider')) {
    await expect(step(page, 'provider')).toHaveAttribute('data-state', 'open')
    await expect(step(page, 'provider').getByTestId('setup-detail')).toContainText('has no credential yet')
    // The key saved on its screen: the step ticks itself.
    expect(await call(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
    await page.reload()
    await expect(step(page, 'provider')).toHaveAttribute('data-state', 'done')
    await expect(step(page, 'provider').getByTestId('setup-detail')).toContainText('has its credential')
    await expect(page.getByTestId('setup-section')).not.toContainText(KEY)
  }
})

test('the home page shows what is next, and a step leads to its screen (GS-02, GS-06)', async ({ page }) => {
  await page.goto('/#/')
  const view = await checklist(page)
  test.skip(view.complete, 'every step is done or dismissed here')
  const next = view.steps.find((s) => s.id === view.next)!
  const card = page.getByTestId('home-setup')
  await expect(card).toBeVisible()
  await expect(card.getByTestId('home-setup-next')).toHaveText(`Next: ${next.title}`)
  await card.getByTestId('home-setup-all').click()
  await expect(page).toHaveURL(/#\/admin\/settings\/start$/)
  await expect(page.getByTestId('setup-section')).toBeVisible()
  // The next step leads, and its Open goes where the step is done.
  await expect(step(page, next.id)).toHaveAttribute('aria-current', 'step')
  await step(page, next.id).getByTestId('setup-open').click()
  await expect(page).toHaveURL(new RegExp(next.link.replace(/[/?]/g, '\\$&') + '$'))
  if (next.link.startsWith('#/admin/settings/')) await expect(page.getByTestId('settings-page')).toBeVisible()
})

test('a step is dismissed and brought back, each audited (GS-04)', async ({ page }) => {
  await page.goto('/#/admin/settings/start')
  await expect(page.getByTestId('setup-step')).toHaveCount(9)
  const view = await checklist(page)
  const open = view.steps.filter((s) => s.state === 'open')
  test.skip(open.length === 0, 'no step is open here')
  const target = open[open.length - 1]!
  await step(page, target.id).getByTestId('setup-dismiss').click()
  await expect(step(page, target.id)).toHaveAttribute('data-state', 'dismissed')
  await expect(step(page, target.id)).toContainText('Dismissed by')
  await step(page, target.id).getByTestId('setup-restore').click()
  await expect(step(page, target.id)).toHaveAttribute('data-state', 'open')
  await expect
    .poll(async () => (await cluster.auditLog()).filter((e) => e.action === 'setup.dismiss' && e.target === target.id).map((e) => String(e.detail)))
    .toEqual(expect.arrayContaining([expect.stringMatching(/dismissed$/), expect.stringMatching(/brought back$/)]))
})

test('Sherlock’s setup skill answers what is next from the checklist (GS-07, GS-08)', async ({ page }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  expect(await call(page, '/api/v1/sherlock/key', { key: KEY })).toBe(200)
  expect(await call(page, '/api/v1/sherlock/config', ON)).toBe(200)
  const view = await checklist(page)
  test.skip(view.complete, 'every step is done or dismissed here, so the skill is off')
  await page.reload()
  await expect(page.getByTestId('sherlock-input')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('sherlock-setup')).toBeVisible()
  if (await page.getByTestId('sherlock-new').count()) await page.getByTestId('sherlock-new').click()
  await page.getByTestId('sherlock-input').fill('What’s next?')
  await page.getByTestId('sherlock-input').press('Enter')
  const answer = page.getByTestId('sherlock-answer').last()
  await expect(answer).toHaveAttribute('data-status', 'done', { timeout: 30_000 })
  const next = view.steps.find((s) => s.id === view.next)!
  await expect(answer.getByTestId('sherlock-text')).toContainText(`Next is step ${next.n} of 9, ${next.title}.`)
  await expect(answer.getByTestId('sherlock-steps')).toContainText('Read Getting started')
  // Its link opens the step's screen.
  await expect(answer.getByTestId('sherlock-console-link').first()).toHaveAttribute('href', next.link)
})
