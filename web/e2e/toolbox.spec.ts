// The Toolbox (Phase 9): the Tools button beside a pod or a node opens a
// menu of tools; each opens a dialog, and sending it opens a session as a
// tab in the dock: the person's own dialog is the approval, and only a
// second person's goes through Watson. The menu's fixed changes (Cordon,
// Drain, Delete) run at once. Against the simulator, exec is a pretend
// shell; port forwards are not simulated.
import { expect, test } from './test'
import { inKind } from './cluster'

test.describe.configure({ mode: 'serial' })

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
const SHERLOCK_ON = { clusters: { local: true }, model: 'claude-opus-5-5', dailyUSD: 20, monthlyUSD: 200 }
const shots = process.env.E2E_SHOTS

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await page.goto('/#/pods', { waitUntil: 'domcontentloaded' })
  await page.request.put('/api/v1/watson/config', { data: { twoPerson: false }, headers: SAME })
  await page.request.post('/api/v1/watson/clear', { headers: SAME })
  const on = await page.request.put('/api/v1/sherlock/config', { data: SHERLOCK_ON, headers: SAME })
  expect(on.status(), await on.text()).toBe(200)
  await page.evaluate(() => localStorage.removeItem('221b.dock'))
  await page.close()
})

test('a shell opens from its dialog and runs in the dock', async ({ page }) => {
  test.skip(inKind, 'the pretend shell is the simulator’s')
  await page.goto('/#/pods', { waitUntil: 'domcontentloaded' })
  const row = page.locator('[data-testid^="tools-pod-"]').first()
  await expect(row).toBeAttached({ timeout: 20_000 })
  await row.hover()
  await row.click()
  const menu = page.getByTestId('tools-menu')
  await expect(menu).toBeVisible()
  await expect(menu.getByTestId('tool-shell')).not.toHaveAttribute('aria-disabled', 'true', { timeout: 10_000 })
  if (shots) await page.screenshot({ path: `${shots}/pod-menu.png` })
  await menu.getByTestId('tool-shell').click()
  const dlg = page.getByTestId('tool-dialog')
  await expect(dlg).toBeVisible()
  await expect(dlg.getByRole('heading')).toContainText('Shell in pod')
  // Not production: no reason, no approval note, no card.
  await expect(dlg.getByLabel('Reason')).toHaveCount(0)
  await expect(dlg.getByTestId('tool-send')).toHaveText('Open shell')
  if (shots) await page.screenshot({ path: `${shots}/shell-dialog.png` })
  await dlg.getByTestId('tool-send').click()
  await expect(dlg).toHaveCount(0)

  const term = page.getByTestId('session-terminal')
  await expect(term).toBeVisible({ timeout: 20_000 })
  await expect(term).toContainText('root@', { timeout: 20_000 })
  await term.click()
  await page.keyboard.type('id')
  await page.keyboard.press('Enter')
  await expect(term).toContainText('uid=0(root)')
  await page.keyboard.type('cat /etc/resolv.conf')
  await page.keyboard.press('Enter')
  await expect(term).toContainText('nameserver')
  if (shots) await page.screenshot({ path: `${shots}/dock-shell.png` })

  await page.getByTestId('dock-tab-sessions').click()
  await expect(page.getByTestId('sessions-pane')).toBeVisible()
  if (shots) await page.screenshot({ path: `${shots}/dock-sessions.png` })
  await page.getByTestId('sessions-pane').getByRole('button', { name: 'Open' }).first().click()
  await page.getByTestId('session-end').click()
  await expect(page.getByTestId('session-pane')).toContainText(/Ended by its owner|ended/i, { timeout: 10_000 })
})

test('the pod menu: tools, a port forward with the pod’s ports, and Delete', async ({ page }) => {
  await page.goto('/#/pods', { waitUntil: 'domcontentloaded' })
  const btn = page.locator('[data-testid^="tools-pod-"]').first()
  await expect(btn).toBeAttached({ timeout: 20_000 })
  await btn.hover()
  await btn.click()
  const menu = page.getByTestId('tools-menu')
  await expect(menu.getByTestId('tool-shell')).not.toHaveAttribute('aria-disabled', 'true', { timeout: 10_000 })
  await expect(menu.getByRole('menuitem')).toHaveText([/^Shell/, /^Port forward/, /^Debug container/, /^Delete/])
  await expect(menu).not.toContainText('Watson')
  await menu.getByTestId('tool-forward').click()
  const dlg = page.getByTestId('tool-dialog')
  await expect(dlg.getByRole('heading')).toContainText('Port forward from pod')
  await dlg.getByRole('button', { name: 'Cancel' }).click()
  await expect(dlg).toHaveCount(0)

  await btn.click()
  await menu.getByTestId('tool-delete').click()
  const confirm = page.getByTestId('confirm')
  await expect(confirm).toContainText('Delete pod')
  // Focus starts on Cancel, so Enter never deletes by accident.
  await expect(confirm.getByRole('button', { name: 'Cancel' })).toBeFocused()
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(confirm).toHaveCount(0)
})

test('a node’s menu cordons it at once, without a card to approve', async ({ page }) => {
  await page.goto('/#/nodes', { waitUntil: 'domcontentloaded' })
  const btn = page.locator('[data-testid^="tools-node-"]').nth(3)
  await expect(btn).toBeAttached({ timeout: 20_000 })
  const node = ((await btn.getAttribute('data-testid')) ?? '').replace('tools-node-', '')
  await btn.hover()
  await btn.click()
  const menu = page.getByTestId('tools-menu')
  await expect(menu.getByTestId('tool-node-shell')).toBeVisible()
  await expect(menu.getByTestId('tool-node-command')).toHaveCount(0)
  if (shots) await page.screenshot({ path: `${shots}/node-menu.png` })
  const cordon = menu.getByTestId('tool-cordon')
  const undo = (await cordon.textContent())?.startsWith('Uncordon') ? 'cordon' : 'uncordon'
  await cordon.click()
  await expect(page.getByText(new RegExp(`(Cordoning|Uncordoning) node ${node}`))).toBeVisible({ timeout: 10_000 })
  // Put the node back as it was, the same way.
  const back = await page.request.post('/api/v1/watson/actions', { data: { action: undo, node }, headers: SAME })
  expect(back.status(), await back.text()).toBe(202)
  expect((await back.json()).status).not.toBe('pending')
})

test('a node shell opens without a second approval off production, and Esc cancels its dialog', async ({ page }) => {
  await page.goto('/#/nodes', { waitUntil: 'domcontentloaded' })
  const btn = page.locator('[data-testid^="tools-node-"]').first()
  await expect(btn).toBeAttached({ timeout: 20_000 })
  await btn.hover()
  await btn.click()
  await page.getByTestId('tools-menu').getByTestId('tool-node-shell').click()
  const dlg = page.getByTestId('tool-dialog')
  await expect(dlg.getByRole('radio', { name: /^Restricted/ })).toBeChecked()
  await expect(dlg.getByText('Reason (optional)')).toBeVisible()
  await expect(dlg.getByTestId('tool-send')).toHaveText('Open node shell')
  await expect(dlg).not.toContainText('second')
  if (shots) await page.screenshot({ path: `${shots}/node-shell.png` })
  await page.keyboard.press('Escape')
  await expect(dlg).toHaveCount(0)
})
