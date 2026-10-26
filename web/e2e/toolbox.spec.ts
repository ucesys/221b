// The Toolbox (Phase 9): the Tools button beside a pod or a node opens a
// menu of tools; each opens a dialog whose request becomes a Watson card,
// and once approved a session opens as a tab in the dock. Against the
// simulator, exec is a pretend shell; port forwards are not simulated.
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

test('a shell opens through a Watson card and runs in the dock', async ({ page }) => {
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
  // Not production: no reason, no approval note.
  await expect(dlg.getByLabel('Reason')).toHaveCount(0)
  if (shots) await page.screenshot({ path: `${shots}/shell-dialog.png` })
  await dlg.getByTestId('tool-send').click()
  await expect(dlg).toHaveCount(0)

  const card = page.getByTestId('watson-card').filter({ hasText: 'Opens a session' }).last()
  await expect(card).toBeVisible({ timeout: 20_000 })
  await expect(card.getByTestId('watson-approve')).toBeEnabled({ timeout: 30_000 })
  if (shots) await page.screenshot({ path: `${shots}/shell-card.png` })
  await card.getByTestId('watson-approve').click()

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

test('a node’s menu runs one command on it through Watson', async ({ page }) => {
  test.skip(inKind, 'the canned output is the simulator’s')
  await page.goto('/#/nodes', { waitUntil: 'domcontentloaded' })
  const btn = page.locator('[data-testid^="tools-node-"]').nth(3)
  await expect(btn).toBeAttached({ timeout: 20_000 })
  await btn.hover()
  await btn.click()
  const menu = page.getByTestId('tools-menu')
  await expect(menu.getByTestId('tool-node-command')).not.toHaveAttribute('aria-disabled', 'true', { timeout: 10_000 })
  await menu.getByTestId('tool-node-command').hover()
  if (shots) await page.screenshot({ path: `${shots}/node-menu.png` })
  await menu.getByRole('menuitem', { name: /^df -h/ }).click()
  const dlg = page.getByTestId('tool-dialog')
  await expect(dlg).toBeVisible()
  await expect(dlg.getByRole('textbox', { name: 'Command' })).toHaveValue(/^df -h/)
  // A node command needs a second SuperAdmin on every cluster: a reason too.
  await dlg.getByLabel('Reason').fill('Disk pressure warnings on this node since 09:40')
  if (shots) await page.screenshot({ path: `${shots}/node-command-dialog.png` })
  await dlg.getByTestId('tool-send').click()
  const card = page.getByTestId('watson-card').filter({ hasText: 'Opens a session' }).last()
  await expect(card).toBeVisible({ timeout: 20_000 })
  await expect(card).toContainText('df -h')
  if (shots) await page.screenshot({ path: `${shots}/node-card.png` })
})
