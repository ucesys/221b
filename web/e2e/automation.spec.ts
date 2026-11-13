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
  for (const name of ['Nodes', 'Scripts', 'Variables', 'Run profiles', 'Run history']) await expect(views.getByRole('link', { name, exact: true })).toBeVisible()
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
  await expect(views.getByRole('link', { name: 'Run history' })).toHaveAttribute('aria-current', 'page')
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
  await pane
    .locator('.rk-fl', { hasText: `group 0, ${stamp}` })
    .getByTestId('script-group-pick')
    .click()
  await expect(row(names[0]!)).toBeVisible()
  await expect(row(names[1]!)).toHaveCount(0)
  await pane
    .locator('.rk-fl', { hasText: `group 1, ${stamp}` })
    .getByTestId('script-group-check')
    .check()
  await expect(row(names[1]!)).toBeVisible()
  await expect(page.locator('table')).toHaveCount(1)
  await pane.getByTestId('script-group-all').click()
  const edit = async (i: number) => {
    await pane
      .locator('.rk-fl', { hasText: `group ${i}, ${stamp}` })
      .getByTestId('script-group-edit')
      .click()
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

test("a node's details show what its Kubernetes agent keeps from pods, read-only", async ({ page }) => {
  test.skip(inKind, "the simulator's kubelets report reservations and an eviction threshold; kind's keep none")
  await page.goto('/#/nodes')
  await page.locator('[data-node]').first().focus()
  await page.keyboard.press('Enter')
  const memory = page.getByTestId('resource-memory')
  for (const k of ['Total on the node', 'Reserved for the system', 'Reserved for Kubernetes', 'Eviction threshold', 'Allocatable', 'Requested', 'Free to schedule']) await expect(memory.locator('dt', { hasText: k })).toBeVisible()
  await expect(page.getByTestId('node-agent')).toContainText('Read-only')
  await expect(page.getByTestId('resource-cpu').locator('input, select, textarea, button')).toHaveCount(0)
})

test('Run profiles lists who a script runs as, and a new one can be made and removed', async ({ page }) => {
  const views = page.getByRole('navigation', { name: 'Views' })
  await page.goto('/#/scripts')
  await views.getByRole('link', { name: 'Run profiles', exact: true }).click()
  await expect(page).toHaveURL(/#\/run-profiles/)
  const table = page.getByTestId('profiles-table')
  for (const [id, name, kind] of [
    ['read-only', 'nobody', 'ssh'],
    ['login-user', 'ssh-paired account', 'ssh'],
    ['as-the-person', 'login-user', 'k8s'],
  ] as const) {
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

// FU-04, FU-06: a run parameter starts bound to the variable of its name,
// which is not sent; another is searched by a variable's name and bound to
// it, so the run gives each node its own; a typed value is a constant.
test('a run parameter binds to a variable picked by name, and the run says so', async ({ page }) => {
  const SAME = { 'Sec-Fetch-Site': 'same-origin' }
  const stamp = Date.now()
  const name = `e2e-bind-${stamp}`
  const variable = `E2E_ZONE_${stamp}`
  const before = (await (await page.request.get('/api/v1/automation/fleets')).json()).fleets.find((f: { id: string }) => f.id === '_all').params
  const put = await page.request.put('/api/v1/automation/fleets/_all/variables', { headers: SAME, data: { ...before, values: { ...(before.values ?? {}), [variable]: 'zone-b' } } })
  expect(put.status(), await put.text()).toBe(200)
  try {
    await page.goto('/#/scripts')
    await page.getByTestId('script-new').click()
    await page.getByTestId('script-name').fill(name)
    await page.getByTestId('script-text').fill(`#!/bin/sh
# 221b:describe Read the pods
# 221b:target kubernetes
# 221b:privilege none
# 221b:dry-run yes
# 221b:k8s get pods
# 221b:param TARGET_ZONE optional where
# 221b:param ${variable} optional the same zone
# 221b:param MODE optional how
kubectl get pods -A
`)
    await page.getByTestId('script-save').click()
    await expect(page.getByTestId('script-submit')).toBeVisible()
    await page.getByTestId('script-drawer-run').click()
    // A variable of its name: the parameter starts bound to it, as its chip.
    const auto = page.getByTestId(`run-param-${variable}-binding`)
    await expect(auto).toHaveAttribute('data-auto', 'true')
    await expect(auto).toContainText(`variable ${variable} · zone-b`)
    await expect(auto.getByRole('button', { name: `Clear variable ${variable} from ${variable}, to type a constant` })).toBeVisible()
    const box = page.getByTestId('run-param-TARGET_ZONE')
    // No variable of its name: the box is empty, and typing searches every variable by name.
    await expect(box).toHaveValue('')
    await expect(page.getByTestId('run-param-TARGET_ZONE-badge')).toHaveCount(0)
    // FU-07: untouched, with nothing to inherit, it says the script gets no value.
    await expect(page.getByTestId('run-param-MODE-empty')).toHaveAttribute('data-empty', 'unset')
    await expect(page.getByTestId('run-param-MODE-empty')).toHaveText('not set')
    await box.fill(`e2e_zone_${stamp}`)
    const option = page.locator(`[data-testid=run-param-TARGET_ZONE-option][data-bind=${variable}]`)
    await expect(option).toContainText('zone-b')
    await box.press('ArrowDown')
    await box.press('Enter')
    const chip = page.getByTestId('run-param-TARGET_ZONE-binding')
    await expect(chip).toHaveAttribute('data-variable', variable)
    await expect(page.getByTestId('run-param-TARGET_ZONE-spread')).toHaveText('zone-b')
    // Typed, a value is a constant, and says so inside its field.
    await page.getByTestId('run-param-MODE').fill('fast')
    await expect(page.getByTestId('run-param-MODE-badge')).toHaveText('constant')
    await expect(page.getByTestId('run-param-MODE-empty')).toHaveCount(0)
    // FU-07: a chip cleared and the box left empty sends the empty string, and says so; its ✕ goes back.
    await page.getByTestId(`run-param-${variable}-unbind`).click()
    const empty = page.getByTestId(`run-param-${variable}-empty`)
    await expect(empty).toHaveAttribute('data-empty', 'empty')
    await expect(empty).toContainText('empty string ""')
    await page.getByTestId(`run-param-${variable}-empty-clear`).click()
    await expect(auto).toHaveAttribute('data-auto', 'true')
    await page.getByTestId(`run-param-${variable}-unbind`).click()
    await expect(empty).toBeVisible()
    await expect(page.getByTestId(`run-param-${variable}-source`)).toHaveText('this run’s, on every node')
    await page.getByTestId('run-start').click()
    await page.getByTestId('confirm-ok').click()
    await expect(page.getByTestId('run-state')).toHaveText('succeeded')
    // The run's own values are the binding and the constant; the automatic binding was not sent.
    const origin = page.getByTestId('run-param-origin')
    await expect(origin).toHaveCount(3)
    await expect(origin.filter({ hasText: 'empty string' })).toHaveCount(1)
    await expect(page.locator(`[data-testid=run-param-origin][data-bound=${variable}]`)).toHaveText(`variable ${variable}`)
    await expect(origin.filter({ hasText: 'constant' })).toHaveCount(1)
    await expect(page.getByTestId('run-parameters')).toContainText('zone-b')
    await page.getByTestId('run-drawer').getByRole('button', { name: 'Close run' }).click()
  } finally {
    await page.request.delete(`/api/v1/automation/scripts/${name}`, { headers: SAME })
    await page.request.put('/api/v1/automation/fleets/_all/variables', { headers: SAME, data: before })
  }
})

// FU-05: on production the dialog says which of a run's own values wait for another admin's approval.
test('on production the Run dialog says which values wait for approval, and asks for it', async ({ page }) => {
  const SAME = { 'Sec-Fetch-Site': 'same-origin' }
  const name = `e2e-prodvals-${Date.now()}`
  await page.route('**/automation/fleets', async (route) => {
    const res = await route.fetch()
    route.fulfill({ response: res, json: { ...(await res.json()), production: true } })
  })
  await page.route('**/automation/runs/weigh', (route) => route.fulfill({ json: { needsApproval: true, changed: ['MODE'], impact: [{ script: name, version: 1, decision: 'approval', why: 'MODE is marked sensitive', by: 'rule' }] } }))
  try {
    await page.goto('/#/scripts')
    await page.getByTestId('script-new').click()
    await page.getByTestId('script-name').fill(name)
    await page.getByTestId('script-text').fill(`#!/bin/sh
# 221b:describe Read the pods
# 221b:target kubernetes
# 221b:privilege none
# 221b:dry-run yes
# 221b:k8s get pods
# 221b:param MODE optional how
kubectl get pods -A
`)
    await page.getByTestId('script-save').click()
    await expect(page.getByTestId('script-submit')).toBeVisible()
    // Production runs approved versions only, which takes a second person: the page is told v1 is.
    await page.route(`**/api/v1/automation/scripts/${name}`, async (route) => {
      const res = await route.fetch()
      const sc = await res.json()
      sc.versions[0] = { ...sc.versions[0], state: 'approved', approvals: [{ by: { name: 'Bea', user: 'bea' }, at: new Date().toISOString() }], may: { ...sc.versions[0].may, runProduction: true } }
      await route.fulfill({ response: res, json: sc })
    })
    // Opened again, the script is read as the page is told.
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('script-drawer')).toHaveCount(0)
    await page.locator(`[data-testid=script-row][data-name=${name}]`).getByTestId('script-open').click()
    await page.getByTestId('script-drawer-run').click()
    await expect(page.getByTestId('run-production')).toContainText('if one needs approval, the run waits for another admin')
    // A box takes a value on production now; the dialog says it will wait.
    await page.getByTestId('run-param-MODE').fill('wipe')
    await expect(page.getByTestId('run-weigh')).toContainText('need another admin’s approval before the run starts: MODE (MODE is marked sensitive)')
    await page.keyboard.press('Escape')
  } finally {
    await page.unroute('**/automation/fleets')
    await page.unroute('**/automation/runs/weigh')
    await page.unroute(`**/api/v1/automation/scripts/${name}`)
    await page.request.delete(`/api/v1/automation/scripts/${name}`, { headers: SAME })
  }
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

test('Scripts and Run history sort by a column, and Run history comes last in the menu', async ({ page }) => {
  await page.goto('/#/scripts')
  const views = page.getByRole('navigation', { name: 'Views' })
  await expect(views.getByRole('link').last()).toHaveText('Run history')
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
  await views.getByRole('link', { name: 'Run history', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Run history' })).toBeVisible()
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
  await expect(page.getByRole('heading', { name: 'Run history' })).toBeVisible()
})

test('Approvals moved to AIOps › Inbox, where the old Administration link opens it, with nothing to approve on it', async ({ page }) => {
  await page.goto('/#/admin/approvals')
  await expect(page).toHaveURL(/#\/aiops\/inbox\?kind=approvals$/)
  // The Inbox is one table now (IN-01), with no Kind filter since wave 6c (CF-38): the old group's name is let go.
  await expect(page.getByTestId('inbox-kinds')).toHaveCount(0)
  const grid = page.getByRole('grid', { name: 'Inbox' })
  await expect(grid).toBeVisible()
  await expect(grid.getByRole('button', { name: 'Approve' })).toHaveCount(0)
  await page.goto('/#/admin/roles')
  await expect(page.locator('[data-admin-page=roles]')).toBeVisible()
  await expect(page.locator('[data-admin-page=approvals]')).toHaveCount(0)
})

test('Approvals lists what waits on top, then what was decided, each opening with who asked and who approved', async ({ page, request }) => {
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
  await page.goto('/#/aiops/inbox?show=all')
  await expect(page.getByRole('grid', { name: 'Inbox' })).toBeVisible()
  // Pending approvals come before decided ones (IN-03); no Kind filter (CF-38), so the approvals' rows are read.
  await expect(page.getByText('Loading the Inbox…')).toHaveCount(0)
  const states = await page.locator('[data-inbox-row^="approval:"]').getByTestId('inbox-row-status').allTextContents()
  const firstDecided = states.findIndex((x) => x !== 'Pending')
  expect(firstDecided === -1 || !states.slice(firstDecided).includes('Pending')).toBe(true)
  // A row opens with who asked; one waiting is reviewed where it lives.
  const at = new Date().toISOString()
  await page.route('**/api/v1/admin/approvals', (route) => route.fulfill({ json: { items: [{ kind: 'script', state: 'pending', what: `${name} v1`, requestedBy: 'Bea', requestedAt: at, script: name, version: 1 }] } }))
  await page.reload()
  const row = page.locator(`[data-inbox-row="approval:script-${name}-v1"]`)
  await expect(row.getByTestId('inbox-row-status')).toHaveText('Pending')
  await row.locator('[role=rowheader]').click()
  await expect(page).toHaveURL(new RegExp(`#/aiops/inbox/approval/script-${name}-v1\\?show=all$`))
  const drawer = page.getByTestId('approval-drawer')
  await expect(drawer).toContainText('Requested by')
  await expect(drawer).toContainText('Bea')
  await expect(drawer.getByTestId('approval-script-review')).toHaveText('Review')
  await page.unroute('**/api/v1/admin/approvals')
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

test('a decided version opens in place in the Inbox, under All', async ({ page }) => {
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
    route.fulfill({
      json: {
        items: [{ kind: 'script', state: 'rejected', what: `${name} v1`, requestedBy: 'Ada Admin', requestedAt: decidedAt, approvedBy: 'Bea', approvedAt: decidedAt, script: name, version: 1, comment: 'pin the version', mine: true }],
      },
    }),
  )
  await page.goto('/#/aiops/inbox?show=all')
  // There is no yellow i any more: the bell is the Inbox's (CF-80).
  await expect(page.getByTestId('admin-flag')).toHaveCount(0)
  const row = page.locator(`[data-inbox-row="approval:script-${name}-v1"]`)
  await expect(row.getByTestId('inbox-row-status')).toHaveText('Rejected')
  await row.locator('[role=rowheader]').click()
  const drawer = page.getByTestId('approval-version')
  await expect(drawer).toBeVisible()
  await expect(drawer).toContainText('pin the version')
  await expect(drawer.getByTestId('version-text')).toBeVisible()
  await expect(page).toHaveURL(new RegExp(`#/aiops/inbox/approval/script-${name}-v1\\?show=all$`))
  await page.keyboard.press('Escape')
  await expect(drawer).toHaveCount(0)
  await expect(page).toHaveURL(/#\/aiops\/inbox\?show=all$/)
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

test('the bell says what waits for you and opens it in the Inbox; the gear opens Administration (IN-07, CF-80)', async ({ page }) => {
  const at = new Date().toISOString()
  await page.route('**/api/v1/admin/approvals', (route) => route.fulfill({ json: { items: [{ kind: 'script', state: 'pending', what: 'someone-else v1', requestedBy: 'Bea', requestedAt: at, script: 'someone-else', version: 1 }] } }))
  await page.route('**/api/v1/aiops/proposals**', (route) => route.fulfill({ json: { proposals: [], waiting: 0, mayApprove: true } }))
  await page.goto('/#/scripts')
  // No yellow i: the bell, coloured and with no number, stands for what waits (CF-80).
  await expect(page.getByTestId('admin-flag')).toHaveCount(0)
  const bell = page.getByTestId('inbox-bell')
  await expect(bell).toHaveAttribute('data-state', /^(p01|waiting)$/)
  await expect(bell).toHaveAttribute('title', /^\d+ waiting for you(, .+)?: open AIOps › Inbox$/)
  await expect(bell).toHaveText('')
  // Live first, then the bell (CF-41).
  const [l, b] = [await page.getByTestId('live').boundingBox(), await bell.boundingBox()]
  expect(l!.x).toBeLessThan(b!.x)
  await bell.click()
  // The Inbox, with no Kind filter (CF-38).
  await expect(page).toHaveURL(/#\/aiops\/inbox$/)
  await expect(page.locator('[data-inbox-row="approval:script-someone-else-v1"]')).toBeVisible()
  // The bell stays while it waits; the gear itself opens Administration.
  await expect(bell).toHaveAttribute('data-state', /^(p01|waiting)$/)
  await page.getByTestId('admin-link').click()
  await expect(page).toHaveURL(/#\/(c\/[^/]+\/)?admin\//)
  await page.unroute('**/api/v1/admin/approvals')
  await page.unroute('**/api/v1/aiops/proposals**')
})
