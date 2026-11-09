// AI settings, round 2 (Phase 12, wave 5): an in-house endpoint without a
// credential (the owner's "A5000", serving qwen3.8-27b), tested in the
// provider dialog before it is saved, its models in a dropdown from the
// endpoint with their context windows, usable without a key once tested;
// Sherlock on its small model in its compact form; an agent's built-in
// text pre-filled and editable; Draft → Test → Pilot → Live as steps that
// act or say why not; the Policies checkbox fitting the drawer. The
// console talks to the fake Claude API, whose /open/v1 takes no key.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster } from './cluster'

test.describe.configure({ mode: 'serial' })

const KEY = 'sk-ant-e2e-fake-key-0123456789'
const ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }

async function call(page: Page, method: string, path: string, body?: unknown): Promise<number> {
  return page.evaluate(
    async ([m, p, b]) => (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b === undefined ? undefined : JSON.stringify(b) })).status,
    [method, path, body] as const,
  )
}

async function open(page: Page, hash: string) {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

async function agents(page: Page) {
  await open(page, '#/admin/settings/agents')
  await expect(page.getByTestId('agents-table')).toBeVisible()
}

test.afterAll(async ({ browser }) => {
  // Sherlock back on the default provider and off; the A5000 gone.
  const page = await browser.newPage()
  await open(page, '#/nodes')
  await call(page, 'PUT', '/api/v1/ai/agents/sherlock', { sherlockClusters: [] })
  await call(page, 'DELETE', '/api/v1/ai/agents/auditor/draft')
  await call(page, 'DELETE', '/api/v1/ai/providers/a5000')
  await call(page, 'PUT', '/api/v1/sherlock/config', { ...ON, clusters: {} })
  await page.close()
})

test('an in-house endpoint without a key is tested in its dialog, its models listed with their context, and used once tested; Sherlock on it is compact (AS-04 to AS-07)', async ({ page }) => {
  await open(page, '#/nodes')
  expect(await call(page, 'PUT', '/api/v1/sherlock/key', { key: KEY })).toBe(200)
  expect(await call(page, 'PUT', '/api/v1/sherlock/config', ON)).toBe(200)
  // The fake's address as the console reaches it: the default provider's.
  const cfg = (await page.evaluate(async () => (await fetch('/api/v1/sherlock/config')).json())) as { provider: { endpoint: string } }
  const house = cfg.provider.endpoint.replace(/\/$/, '') + '/open/v1'
  await open(page, '#/admin/settings/providers')
  await page.getByTestId('provider-add').click()
  const s = page.getByTestId('provider-drawer')
  await s.getByTestId('provider-name').fill('A5000')
  await s.getByTestId('provider-kind').selectOption('compatible')
  await s.getByTestId('provider-endpoint').fill(house)
  // The dropdown fills from the endpoint, with each model's context.
  await s.getByTestId('provider-model-load').click()
  const list = s.getByTestId('provider-model-list')
  await expect(list.locator('option', { hasText: 'qwen3.8-27b · 16,384 tokens' })).toHaveCount(1)
  await list.selectOption('qwen3.8-27b')
  await expect(s.getByTestId('provider-context')).toHaveValue('16384')
  // Test before saving: it answers without a key.
  await s.getByTestId('provider-drawer-test').click()
  await expect(s.getByTestId('provider-drawer-test-result')).toContainText('qwen3.8-27b answered in')
  await expect(s.getByTestId('provider-drawer-test-result')).toContainText('without a key')
  await s.getByTestId('provider-save').click()
  await expect(s).toHaveCount(0)
  // Saved after a passing test, it was tested again as saved: no key needed.
  const card = page.locator('[data-testid=provider][data-provider=a5000]')
  await expect(card.getByTestId('provider-key-pill')).toHaveText('No key needed')
  await expect(card.getByTestId('provider-key-pill')).toHaveAttribute('data-tone', 'ok')
  await expect(card.getByTestId('provider-last-test')).toContainText('Passed with qwen3.8-27b, without a key')
  await expect(card).toContainText('16,384 tokens of context')

  // Sherlock on it: usable without a key, compact on its small model.
  await agents(page)
  await page.locator('[data-testid=agent-row][data-agent=sherlock] [data-testid=agent-open]').click()
  const drawer = page.getByTestId('agent-drawer')
  await drawer.getByTestId('agent-provider').selectOption('a5000')
  await drawer.locator('[data-testid=agent-cluster][data-cluster=local]').check()
  await drawer.getByTestId('agent-save').click()
  await expect(drawer.getByTestId('agent-save-state')).toHaveText('No changes.')
  const line = drawer.getByTestId('agent-context-line')
  await expect(line).toHaveAttribute('data-compact', 'true')
  await expect(line).toContainText('of qwen3.8-27b’s 16,384 tokens')
  await expect(line).toContainText('so Sherlock answers in its compact form')
  await expect(drawer).not.toContainText('has no credential')
  // Its compact form is pre-filled beside its whole form.
  await drawer.locator('[data-testid=ins-part-tab][data-part=compact]').click()
  await expect(drawer.getByTestId('ins-part')).toHaveValue(/You are Sherlock, the site reliability engineer built into 221B/)
  await page.keyboard.press('Escape')

  // An answer through it sends no view action and no hand-over.
  await cluster.resetClaude()
  await open(page, '#/pods')
  await page.getByTestId('sherlock-input').fill('Why is anything pending?')
  await page.getByTestId('sherlock-input').press('Enter')
  await expect(page.getByTestId('sherlock-answer').last()).toHaveAttribute('data-status', /done|failed/, { timeout: 30_000 })
  const sent = (await cluster.claudeRequests()).map((r) => JSON.parse(r) as { model?: string; tools?: { function?: { name: string } }[] })
  const mine = sent.filter((r) => r.model === 'qwen3.8-27b')
  expect(mine.length).toBeGreaterThan(0)
  const names = mine.flatMap((r) => (r.tools ?? []).map((t) => t.function?.name ?? ''))
  expect(names.filter((n) => n.startsWith('view_') || n === 'hand_over')).toEqual([])
})

test('an agent’s built-in text is pre-filled and edited as a draft, its stages say what each does and why an action waits, and its Policies checkbox fits (AS-01 to AS-03, AS-08)', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 900 })
  await agents(page)
  await page.locator('[data-testid=agent-row][data-agent=auditor] [data-testid=agent-open]').click()
  const drawer = page.getByTestId('agent-drawer')
  const part = drawer.getByTestId('ins-part')
  await expect(part).toHaveValue(/^You are the Auditor/)
  await expect(part).toHaveValue(/Rules every agent of this console keeps/)
  await expect(drawer.getByTestId('ins-part-state')).toHaveText('Built-in')
  // No draft: every step says why its action waits.
  await expect(drawer.locator('[data-testid=ins-stages] [aria-current=step] [data-testid=ins-step-name]')).toHaveText('Live')
  await expect(drawer.getByTestId('ins-test')).toBeDisabled()
  await expect(drawer.locator('[data-testid=ins-stages] [data-stage=live] [data-testid=ins-step-why]')).toContainText('Nothing waits to go live')
  // An edit of the built-in text, compared and saved as a draft.
  const text = await part.inputValue()
  await part.fill('You are the Auditor of Acme. Weigh PCI findings first.\n\n' + text.slice(text.indexOf('Rules every agent')))
  await expect(drawer.getByTestId('ins-part-state')).toHaveText('Edited, not saved')
  await drawer.getByTestId('ins-part-compare').click()
  await expect(drawer.getByTestId('ins-part-diff')).toContainText('+You are the Auditor of Acme.')
  await expect(drawer.locator('[data-testid=ins-stages] [data-stage=test] [data-testid=ins-step-why]')).toHaveText('Save your edits first.')
  await drawer.getByTestId('ins-save-draft').click()
  await expect(drawer.locator('[data-testid=ins-stages] [aria-current=step] [data-testid=ins-step-name]')).toHaveText('Draft')
  await expect(drawer.getByTestId('ins-test')).toBeEnabled()
  await expect(drawer.getByTestId('ins-pilot')).toBeDisabled()
  await expect(drawer.locator('[data-testid=ins-stages] [data-stage=pilot] [data-testid=ins-step-why]')).toHaveText('Run the tests first: a pilot serves a tested draft.')
  const prompt = (await page.evaluate(async () => (await fetch('/api/v1/ai/agents/auditor/prompt?draft=1')).json())) as { instructions: string }
  expect(prompt.instructions.startsWith('You are the Auditor of Acme.')).toBe(true)
  // Reset to built-in puts the shipped text back in the field.
  await drawer.getByTestId('ins-part-reset').click()
  await expect(part).toHaveValue(/^You are the Auditor/)
  await expect(drawer.getByTestId('ins-part-state')).toHaveText('Built-in')
  // The Policies checkbox's text wraps inside its column.
  const policies = drawer.locator('label:has([data-testid=agent-tool][data-tool=list_policies])')
  await expect(policies).toContainText('Policies (budgets, quotas, Pod Security, webhooks)')
  const fits = await policies.evaluate((el) => {
    const grid = el.parentElement!.getBoundingClientRect()
    const box = el.getBoundingClientRect()
    return el.scrollWidth <= el.clientWidth + 1 && box.right <= grid.right + 1
  })
  expect(fits).toBe(true)
})
