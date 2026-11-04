import { inKind } from './cluster'
import { expect, test } from './test'

// The Automation area (AU-01) and a script from its first save to review
// (AU-02, AU-05).
test('Automation is an area of its own, and Nodes stays in the area it was opened from', async ({ page }) => {
  await page.goto('/#/alerts')
  const views = page.getByRole('navigation', { name: 'Views' })
  await expect(views.getByRole('link', { name: 'Scripts' })).toHaveCount(0)
  await page.getByTestId('area-automation').click()
  await expect(page.getByTestId('area-automation')).toHaveAttribute('aria-pressed', 'true')
  await expect(page).toHaveURL(/#\/scripts/)
  for (const name of ['Nodes', 'Scripts', 'Variables', 'Run Profiles', 'Run History']) await expect(views.getByRole('link', { name, exact: true })).toBeVisible()
  await views.getByRole('link', { name: 'Nodes', exact: true }).click()
  await expect(page).toHaveURL(/#\/nodes/)
  await expect(page.getByTestId('area-automation')).toHaveAttribute('aria-pressed', 'true')
  await expect(views.getByRole('link', { name: 'Scripts' })).toBeVisible()
  await page.getByTestId('area-kubernetes').click()
  await expect(page).toHaveURL(/#\/alerts/)
  await expect(views.getByRole('link', { name: 'Scripts' })).toHaveCount(0)
  await views.getByRole('link', { name: 'Nodes', exact: true }).click()
  await expect(page.getByTestId('area-kubernetes')).toHaveAttribute('aria-pressed', 'true')
  await page.goto('/#/automation/runs')
  await expect(views.getByRole('link', { name: 'Run History' })).toHaveAttribute('aria-current', 'page')
  // Administration keeps the area it was opened from.
  await page.getByTestId('admin-link').click()
  await expect(page.getByTestId('admin-page')).toBeVisible()
  await expect(page.getByTestId('area-automation')).toHaveAttribute('aria-pressed', 'true')
  await expect(views.getByRole('link', { name: 'Scripts' })).toBeVisible()
})

test('groups filter one Scripts table from the left, as fleets do on Nodes', async ({ page }) => {
  const stamp = Date.now()
  const names = [`e2e-a-${stamp}`, `e2e-b-${stamp}`]
  await page.goto('/#/scripts')
  for (let i = 0; i < 2; i++) {
    await page.getByTestId('script-group-new').click()
    await page.getByTestId('group-name').fill(`group ${i}, ${stamp}`)
    await page.getByTestId('group-save').click()
    await expect(page.getByTestId('group-drawer')).toHaveCount(0)
  }
  for (const [i, name] of names.entries()) {
    await page.getByTestId('script-new').click()
    await page.getByTestId('script-name').fill(name)
    await page.getByTestId('script-group').selectOption(`group ${i}, ${stamp}`)
    await page.getByTestId('script-save').click()
    await expect(page.getByTestId('script-submit')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('script-drawer')).toHaveCount(0)
  }
  const pane = page.getByTestId('script-groups')
  const row = (name: string) => page.locator(`[data-testid=script-row][data-name=${name}]`)
  await pane.locator('.rk-fl', { hasText: `group 0, ${stamp}` }).getByTestId('script-group-pick').click()
  await expect(row(names[0]!)).toBeVisible()
  await expect(row(names[1]!)).toHaveCount(0)
  await pane.locator('.rk-fl', { hasText: `group 1, ${stamp}` }).getByTestId('script-group-check').check()
  await expect(row(names[1]!)).toBeVisible()
  await expect(page.locator('table')).toHaveCount(1)
  await pane.getByTestId('script-group-all').click()
  const edit = async (i: number) => {
    await pane.locator('.rk-fl', { hasText: `group ${i}, ${stamp}` }).getByTestId('script-group-edit').click()
    await expect(page.getByTestId('group-drawer')).toBeVisible()
  }
  await edit(0)
  await expect(page.getByTestId('group-delete')).toBeDisabled()
  await expect(page.getByTestId('group-scripts')).toContainText(names[0]!)
  await page.getByTestId('group-drawer').getByRole('button', { name: 'Cancel' }).click()
  for (const name of names) {
    await row(name).getByTestId('script-open').click()
    await page.getByTestId('script-delete').click()
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByTestId('script-drawer')).toHaveCount(0)
  }
  for (const i of [0, 1]) {
    await edit(i)
    await page.getByTestId('group-delete').click()
    await expect(page.getByTestId('group-drawer')).toHaveCount(0)
  }
})

test('a check goes to its line, and the script and its checks stand side by side', async ({ page }) => {
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  const text = page.getByTestId('script-text')
  await text.fill('#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho one\nsudo reboot\n')
  await page.getByTestId('finding-line').first().waitFor()
  const line = page.getByTestId('finding-line').filter({ hasText: 'line 5' }).first()
  await line.click()
  await expect(text).toBeFocused()
  const picked = await text.evaluate((el: HTMLTextAreaElement) => el.value.slice(el.selectionStart, el.selectionEnd))
  expect(picked).toBe('sudo reboot')
  const code = await text.boundingBox()
  const checks = await page.getByTestId('auto-findings').boundingBox()
  expect(checks!.x).toBeGreaterThan(code!.x + code!.width - 1)
})

test('the Run dialog keeps a large fleet in a list of its own size', async ({ page }) => {
  test.skip(inKind, 'the simulator has the 40 nodes; kind has three')
  const name = `e2e-${Date.now()}`
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-name').fill(name)
  await page.getByTestId('script-save').click()
  await page.getByTestId('script-drawer-run').click()
  await page.getByTestId('run-fleet').selectOption('_all')
  // Run as shows the profile the script runs as, not a placeholder.
  await expect(page.getByTestId('run-config').locator('option:checked')).toHaveText('ssh-paired account')
  const list = page.getByTestId('run-nodes')
  await expect(list.getByRole('checkbox')).toHaveCount(40)
  const box = await list.locator('..').boundingBox()
  expect(box!.height).toBeLessThanOrEqual(190)
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('run-dialog')).toHaveCount(0)
  await page.getByTestId('script-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
})

test('the script that reserves resources passes the checks and can go to review', async ({ page }) => {
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-template').selectOption('reserve')
  const name = `e2e-reserve-${Date.now()}`
  await page.getByTestId('script-name').fill(name)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-submit')).toBeEnabled()
  await expect(page.getByTestId('script-header')).toContainText('SYSTEM_RESERVED_MEMORY')
  await page.getByTestId('script-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
})

test('a node\'s details show what its Kubernetes agent keeps from pods, read-only', async ({ page }) => {
  test.skip(inKind, 'the simulator\'s kubelets report reservations and an eviction threshold; kind\'s keep none')
  await page.goto('/#/nodes')
  await page.locator('[data-node]').first().focus()
  await page.keyboard.press('Enter')
  const memory = page.getByTestId('resource-memory')
  for (const k of ['Total on the node', 'Reserved for the system', 'Reserved for Kubernetes', 'Eviction threshold', 'Allocatable', 'Requested', 'Free to schedule']) await expect(memory.locator('dt', { hasText: k })).toBeVisible()
  await expect(page.getByTestId('node-agent')).toContainText('Read-only')
  await expect(page.getByTestId('resource-cpu').locator('input, select, textarea, button')).toHaveCount(0)
})

test('Run Profiles lists who a script runs as, and a new one can be made and removed', async ({ page }) => {
  const views = page.getByRole('navigation', { name: 'Views' })
  await page.goto('/#/scripts')
  await views.getByRole('link', { name: 'Run Profiles', exact: true }).click()
  await expect(page).toHaveURL(/#\/run-profiles/)
  const table = page.getByTestId('profiles-table')
  for (const [id, name, kind] of [['read-only', 'nobody', 'ssh'], ['login-user', 'ssh-paired account', 'ssh'], ['as-the-person', 'login-user', 'k8s']] as const) {
    await expect(table.locator(`[data-id="${id}"]`)).toContainText(name)
    await expect(table.locator(`[data-id="${id}"]`).getByTestId('config-kind')).toHaveText(kind)
  }
  await expect(table.locator('th', { hasText: 'Connects as' })).toHaveCount(0)
  // The fleets' ceilings come before the table.
  const above = await page.getByTestId('config-ceilings').evaluate((el, t) => !!(el.compareDocumentPosition(t!) & Node.DOCUMENT_POSITION_FOLLOWING), await table.elementHandle())
  expect(above).toBe(true)
  const id = `e2e-${Date.now()}`
  await page.getByTestId('config-new').click()
  await page.getByTestId('config-id').fill(id)
  await page.getByTestId('config-name').fill(id)
  await page.getByTestId('config-privilege').selectOption('full')
  await page.getByTestId('config-save').click()
  const row = table.locator(`[data-id="${id}"]`)
  await expect(row).toContainText('elevated')
  await row.getByTestId('config-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(row).toHaveCount(0)
})

test('a script on the cluster runs through Watson and its output says so', async ({ page }) => {
  const name = `e2e-kube-${Date.now()}`
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-name').fill(name)
  await page.getByTestId('script-text').fill(`#!/bin/sh
# 221b:describe Read the pods
# 221b:target kubernetes
# 221b:privilege none
# 221b:dry-run yes
# 221b:k8s get pods
kubectl get pods -A
`)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-submit')).toBeVisible()
  await page.getByTestId('script-drawer-run').click()
  await expect(page.getByTestId('run-on-cluster')).toContainText('get pods')
  await expect(page.getByTestId('run-dialog')).toBeVisible()
  await expect(page.getByTestId('run-nodes')).toHaveCount(0)
  await expect(page.getByTestId('run-config').locator('option:checked')).toHaveText('login-user')
  await page.getByTestId('run-start').click()
  await expect(page.getByTestId('confirm')).toContainText('not a dry run')
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('run-state')).toHaveText('succeeded')
  await expect(page.getByTestId('run-watson')).toContainText(`Watson via ${name} v1, run #`)
  await expect(page.getByTestId('run-output')).toContainText('Running')
  await page.getByTestId('run-drawer').getByRole('button', { name: 'Close run' }).click()
  await page.locator(`[data-testid=script-row][data-name=${name}]`).getByTestId('script-open').click()
  await page.getByTestId('script-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
})

test('Sherlock is offered on a script, and says so when it is off', async ({ page }) => {
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-sherlock-request').fill('keep chrony running')
  await page.getByTestId('script-sherlock-draft').click()
  await expect(page.getByTestId('script-sherlock-error')).toContainText(/Sherlock/)
})

test('Save says what it needs, and the editor numbers its lines', async ({ page }) => {
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-save-why')).toContainText('name')
  await page.getByTestId('script-name').fill(`e2e-${Date.now()}`)
  await expect(page.getByTestId('script-save-why')).toHaveCount(0)
  await page.getByTestId('script-text').fill('#!/bin/sh\necho one\necho two\n')
  await expect(page.getByTestId('script-text-lines')).toHaveText('1\n2\n3\n4')
})

test('Variables is a table of what each one gets, edited in a drawer', async ({ page }) => {
  test.skip(inKind, 'the simulator has the 40 nodes; kind has three')
  await page.goto('/#/variables')
  const scopes = page.getByTestId('params-scopes')
  await expect(scopes.getByTestId('params-scope').first()).toHaveAttribute('data-id', '_all')
  await expect(scopes.getByTestId('params-node')).toHaveCount(0)
  await page.getByTestId('params-nodes-toggle').click()
  await expect(scopes.getByTestId('params-node')).toHaveCount(40)
  const name = `E2E_${Date.now()}`
  const row = page.locator(`[data-testid=param-row][data-name=${name}]`)

  // On All nodes, then overwritten on one node.
  await page.getByTestId('params-new').click()
  await page.getByTestId('param-name').fill(name)
  await page.getByTestId('param-value').fill('one')
  await page.getByTestId('param-save').click()
  await expect(row).toContainText('one')
  await expect(row).toHaveAttribute('data-red', 'false')

  const first = scopes.getByTestId('params-node').first()
  const nodeName = (await first.getAttribute('data-node'))!
  await first.click()
  await row.getByTestId('param-edit').click()
  await expect(page.getByTestId('param-inherited')).toContainText('All nodes')
  await page.getByTestId('param-value').fill('two')
  await page.getByTestId('param-save').click()
  await expect(row).toContainText('two')
  await expect(row.getByTestId('param-flags')).toContainText('Overwrites All nodes')

  // Back on All nodes it is red, and the drawer names the node.
  await scopes.locator('[data-id="_all"]').click()
  await expect(row).toHaveAttribute('data-red', 'true')
  await expect(row.getByTestId('param-flags')).toContainText('On 1 node')
  await row.getByTestId('param-edit').click()
  await expect(page.getByTestId('param-overrides')).toContainText(nodeName)
  await expect(page.getByTestId('param-scripts')).toContainText('No script reads')
  await page.getByTestId('param-override-delete').click()
  await expect(page.getByTestId('confirm')).toContainText(nodeName)
  await page.getByTestId('confirm-ok').click()
  await expect(row).toHaveAttribute('data-red', 'false')

  // Deleting it takes it off the table.
  await row.getByTestId('param-edit').click()
  await page.getByTestId('param-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(row).toHaveCount(0)
})

test('a fleet can be started from Variables, and it opens there', async ({ page }) => {
  await page.goto('/#/variables')
  const name = `e2e-params-${Date.now()}`
  await page.getByTestId('params-fleet-new').click()
  const editor = page.getByTestId('fleet-editor')
  await editor.getByTestId('fleet-name').fill(name)
  await editor.getByTestId('fleet-text').fill('kubernetes.io/os=linux')
  await editor.getByTestId('fleet-text-add').click()
  await editor.getByTestId('fleet-save').click()
  await expect(editor).toHaveCount(0)
  await expect(page.getByTestId('params-scopes').getByTestId('params-scope').filter({ hasText: name })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('heading', { name })).toBeVisible()
  // As on Nodes: its colour, what it requires, and a pencil that edits it.
  const row = page.getByTestId('params-scopes').locator('.rk-fl', { hasText: name })
  await expect(row.locator('.rk-fdot')).toHaveCount(1)
  await expect(row).toContainText('kubernetes.io/os=linux')
  await expect(row).toContainText('No variables')
  await row.getByTestId('params-fleet-edit').click()
  await expect(editor).toBeVisible()
  await editor.getByRole('button', { name: 'Delete fleet' }).click()
  await editor.getByRole('button', { name: 'Click again to delete' }).click()
  await expect(editor).toHaveCount(0)
  await expect(page.getByTestId('params-scopes').locator('.rk-fl', { hasText: name })).toHaveCount(0)
  await expect(page.getByTestId('params-scope').first()).toHaveAttribute('aria-pressed', 'true')
})

test('Scripts and Run History sort by a column, and Run History comes last in the menu', async ({ page }) => {
  await page.goto('/#/scripts')
  const views = page.getByRole('navigation', { name: 'Views' })
  await expect(views.getByRole('link').last()).toHaveText('Run History')
  const stamp = Date.now()
  const names = [`sort-a-${stamp}`, `sort-b-${stamp}`]
  const api = (method: string, path: string, body?: unknown) =>
    page.evaluate(
      async ([m, p, b]) => void (await fetch(p as string, { method: m as string, headers: { 'Content-Type': 'application/json' }, body: b as string | undefined })),
      [method, path, body === undefined ? undefined : JSON.stringify(body)],
    )
  for (const name of names) await api('POST', '/api/v1/automation/scripts', { name, group: '', text: '#!/bin/sh\necho hi\n' })
  await page.reload()
  await page.getByTestId('scripts-search').fill(`sort-`)
  const rows = page.getByTestId('script-row')
  await expect(rows).toHaveCount(2)
  await expect(rows.first().getByTestId('script-kind')).toHaveText('Shell')
  await expect(rows.first()).toHaveAttribute('data-name', names[0]!)
  const heading = page.getByTestId('scripts-table').locator('[data-col=name]')
  await heading.click()
  await expect(rows.first()).toHaveAttribute('data-name', names[1]!)
  await expect(page.getByTestId('scripts-table').locator('th[aria-sort=descending]')).toContainText('Script')
  await heading.click()
  for (const name of names) await api('DELETE', `/api/v1/automation/scripts/${name}`)
  await views.getByRole('link', { name: 'Run History', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Run History' })).toBeVisible()
})

test('New group in the script drawer opens over it, and the box takes the new group', async ({ page }) => {
  const group = `drawer-${Date.now()}`
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-new-group').click()
  await expect(page.getByTestId('script-drawer')).toBeVisible()
  await page.getByTestId('group-name').fill(group)
  await page.getByTestId('group-save').click()
  await expect(page.getByTestId('group-drawer')).toHaveCount(0)
  await expect(page.getByTestId('script-group')).toHaveValue(group)
  await page.keyboard.press('Escape')
  await page.getByTestId('confirm-ok').click()
  await page.locator('.rk-fl', { hasText: group }).getByTestId('script-group-edit').click()
  await page.getByTestId('group-delete').click()
  await expect(page.getByTestId('group-drawer')).toHaveCount(0)
})

test('Scripts filter by state with a row of buttons, as Nodes does by role', async ({ page }) => {
  await page.goto('/#/scripts')
  const seg = page.getByTestId('scripts-state')
  await expect(seg.getByRole('button')).toHaveText([/^All/, /^Review/, /^Draft/, /^Approved/])
  await seg.locator('[data-state=draft]').click()
  await expect(page).toHaveURL(/state=draft/)
  await expect(seg.locator('[data-state=draft]')).toHaveAttribute('aria-pressed', 'true')
  await seg.locator('[data-state=all]').click()
  await expect(page).not.toHaveURL(/state=/)
})

test('the Automation pages old names still open them', async ({ page }) => {
  await page.goto('/#/parameters')
  await expect(page.getByTestId('params-scopes')).toBeVisible()
  await page.goto('/#/configurations')
  await expect(page.getByTestId('profiles-table')).toBeVisible()
  await page.goto('/#/executions')
  await expect(page.getByRole('heading', { name: 'Run History' })).toBeVisible()
})

test('Approvals is a page of Administration, with nothing to approve on it', async ({ page }) => {
  await page.goto('/#/admin/approvals')
  await expect(page.locator('[data-admin-page=approvals]')).toHaveText('Approvals')
  await expect(page.getByTestId('admin-approvals')).toBeVisible()
  await expect(page.getByTestId('admin-approvals').getByRole('button', { name: 'Approve' })).toHaveCount(0)
})

test('Approvals lists what waits on top, then what was decided, with who asked and who approved', async ({ page, request }) => {
  const name = `e2e-appr-${Date.now()}`
  const SAME = { 'Sec-Fetch-Site': 'same-origin' }
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-name').fill(name)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-submit')).toBeVisible()
  await page.keyboard.press('Escape')
  const sub = await request.post(`/api/v1/automation/scripts/${name}/versions/1/submit`, { headers: SAME })
  expect(sub.status(), await sub.text()).toBe(200)
  await page.goto('/#/admin/approvals')
  const table = page.getByTestId('approvals-table')
  for (const h of ['Requested by', 'Requested on', 'Approved by', 'Approved on']) await expect(table.locator('thead th', { hasText: new RegExp(`^${h}$`) })).toBeVisible()
  // Pending rows come before decided ones.
  const states = await table.locator('tbody tr').evaluateAll((rows) => rows.map((r) => r.getAttribute('data-state')))
  const firstDecided = states.findIndex((x) => x !== 'pending')
  expect(firstDecided === -1 || !states.slice(firstDecided).includes('pending')).toBe(true)
  await page.request.delete(`/api/v1/automation/scripts/${name}`, { headers: SAME })
})

test('closing a script with unsaved changes asks first', async ({ page }) => {
  const name = `e2e-unsaved-${Date.now()}`
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-name').fill(name)
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('script-close-unsaved')).toBeVisible()
  await page.getByTestId('confirm').getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByTestId('script-drawer')).toBeVisible()
  await expect(page.getByTestId('script-name')).toHaveValue(name)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-submit')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
  await page.locator(`[data-testid=script-row][data-name=${name}]`).getByTestId('script-open').click()
  await page.getByTestId('script-text').fill('#!/bin/sh\necho changed\n')
  await page.getByTestId('script-drawer').getByRole('button', { name: 'Close script' }).last().click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
  await page.locator(`[data-testid=script-row][data-name=${name}]`).getByTestId('script-open').click()
  await page.getByTestId('script-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
})

test('Pending approvals sends a reviewer to the version, where it can be read', async ({ page }) => {
  const name = `e2e-${Date.now()}`
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-name').fill(name)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-save-why')).toHaveCount(0)
  await expect(page.getByTestId('script-submit')).toBeVisible()
  await page.keyboard.press('Escape')
  await page.goto(`/#/scripts?script=${name}&version=1`)
  await expect(page.getByTestId('script-drawer')).toBeVisible()
  await expect(page.getByTestId('version-text')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Versions' })).toHaveAttribute('aria-current', 'page')
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
  await expect(page).not.toHaveURL(/script=/)
})

test('a draft keeps its version through its saves, and its author cannot approve it', async ({ page }) => {
  const name = `e2e-${Date.now()}`
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  const box = page.getByTestId('script-name')
  await box.click()
  await page.keyboard.type(name)
  await expect(box).toHaveValue(name)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-save')).toHaveText('Save draft')
  const text = page.getByTestId('script-text')
  await text.fill((await text.inputValue()) + 'echo more\n')
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-submit')).toHaveText('Submit v1 for review')
  await page.getByTestId('script-submit').click()
  await expect(page.getByTestId('script-approve-wait')).toBeVisible()
  await expect(page.getByTestId('script-approve')).toHaveCount(0)
  await page.getByTestId('script-delete').click()
  await page.getByTestId('confirm-ok').click()
  await expect(page.getByTestId('script-drawer')).toHaveCount(0)
})

test('the gear flags a decision on your request and opens Approvals, where a decided version opens in place', async ({ page }) => {
  const name = `e2e-flag-${Date.now()}`
  const SAME = { 'Sec-Fetch-Site': 'same-origin' }
  await page.goto('/#/scripts')
  await page.getByTestId('script-new').click()
  await page.getByTestId('script-name').fill(name)
  await page.getByTestId('script-save').click()
  await expect(page.getByTestId('script-submit')).toBeVisible()
  await page.keyboard.press('Escape')
  // Someone else rejected it: the list is the server's, with one row made up.
  const decidedAt = new Date().toISOString()
  await page.route('**/api/v1/admin/approvals', (route) =>
    route.fulfill({ json: { items: [{ kind: 'script', state: 'rejected', what: `${name} v1`, requestedBy: 'Ada Admin', requestedAt: decidedAt, approvedBy: 'Bea', approvedAt: decidedAt, script: name, version: 1, comment: 'pin the version', mine: true }] } }),
  )
  await page.evaluate(() => localStorage.setItem('221b.approvals.seen', JSON.stringify('2000-01-01T00:00:00Z')))
  await page.goto('/#/scripts')
  await page.reload()
  const gear = page.getByTestId('admin-link')
  await expect(gear.getByTestId('admin-flag')).toBeVisible()
  await gear.click()
  await expect(page).toHaveURL(/#\/admin\/approvals/)
  await expect(gear.getByTestId('admin-flag')).toHaveCount(0)
  const row = page.getByTestId('approval-script')
  await expect(row.getByTestId('approval-mine')).toBeVisible()
  await expect(row.getByTestId('approval-comment')).toHaveText('pin the version')
  await row.getByTestId('approval-script-open').click()
  const drawer = page.getByTestId('approval-version')
  await expect(drawer).toBeVisible()
  await expect(drawer.getByTestId('version-text')).toBeVisible()
  await expect(page).toHaveURL(/#\/admin\/approvals/)
  await page.keyboard.press('Escape')
  await expect(drawer).toHaveCount(0)
  await expect(page).toHaveURL(/#\/admin\/approvals/)
  await page.unroute('**/api/v1/admin/approvals')
  await page.request.delete(`/api/v1/automation/scripts/${name}`, { headers: SAME })
})

test('the privilege ceiling sits above the variables table', async ({ page }) => {
  await page.goto('/#/variables')
  const ceiling = page.locator('section[aria-label="Privilege ceiling"]')
  const vars = page.locator('section[aria-label="Variables"]')
  await expect(ceiling).toBeVisible()
  await expect(vars).toBeVisible()
  const [c, v] = [await ceiling.boundingBox(), await vars.boundingBox()]
  expect(c!.y).toBeLessThan(v!.y)
})

test('a fleet sets how long its rebooted nodes have, and a node its own', async ({ page }) => {
  await page.goto('/#/variables')
  const card = page.getByTestId('params-reboot')
  await expect(card).toContainText('Left empty: 10 minutes')
  await card.getByTestId('params-reboot-minutes').fill('0')
  await expect(card).toContainText('A whole number of minutes, 1 to 240')
  await expect(card.getByTestId('params-reboot-save')).toBeDisabled()
  await card.getByTestId('params-reboot-minutes').fill('25')
  await card.getByTestId('params-reboot-save').click()
  await expect(card.getByTestId('params-reboot-save')).toHaveCount(0)
  await page.getByTestId('params-nodes-toggle').click()
  await page.getByTestId('params-node').first().click()
  await expect(card).toContainText('Left empty: 25 minutes, the longest of its fleets')
  await card.getByTestId('params-reboot-minutes').fill('7')
  await card.getByTestId('params-reboot-save').click()
  await expect(card.getByTestId('params-reboot-save')).toHaveCount(0)
  const node = (await page.getByTestId('params-node').first().getAttribute('data-node'))!
  const fleets = await (await page.request.get('/api/v1/automation/fleets')).json()
  const all = fleets.fleets.find((f: { id: string }) => f.id === '_all')
  expect(all.params.rebootMinutes).toBe(25)
  expect(all.params.nodeReboot[node]).toBe(7)
  // Put it back as it was.
  await page.request.put('/api/v1/automation/fleets/_all/variables', {
    headers: { 'Sec-Fetch-Site': 'same-origin' },
    data: { ...all.params, rebootMinutes: undefined, nodeReboot: undefined },
  })
})

test('the gear flags a new review you can approve, and opening Approvals clears it', async ({ page }) => {
  const at = new Date().toISOString()
  await page.route('**/api/v1/admin/approvals', (route) =>
    route.fulfill({ json: { items: [{ kind: 'script', state: 'pending', what: 'someone-else v1', requestedBy: 'Bea', requestedAt: at, script: 'someone-else', version: 1 }] } }),
  )
  await page.goto('/#/scripts')
  await page.evaluate(() => localStorage.setItem('221b.approvals.seen', JSON.stringify('2000-01-01T00:00:00Z')))
  await page.reload()
  const gear = page.getByTestId('admin-link')
  await expect(gear.getByTestId('admin-flag')).toBeVisible()
  await expect(gear).toHaveAttribute('title', /1 new review for you to approve/)
  await gear.click()
  await expect(page).toHaveURL(/#\/admin\/approvals/)
  await expect(gear.getByTestId('admin-flag')).toHaveCount(0)
  await page.reload()
  await expect(page.getByTestId('approval-script')).toBeVisible()
  await expect(gear.getByTestId('admin-flag')).toHaveCount(0)
  await page.unroute('**/api/v1/admin/approvals')
})
