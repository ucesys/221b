// The Access tab (P4-03): roles and bindings with risk flags, who can do
// what, and each identity's permissions, which must match what the API
// server answers to kubectl auth can-i.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, inKind, kubectlCanI, type KubeObject } from './cluster'

const NS = process.env.E2E_NAMESPACE ?? '221b'
// Two namespaces both clusters have: the console's own in kind, web in the simulator; and default.
const NS1 = inKind ? NS : 'web'
const NS2 = 'default'
const ALICE = 'e2e-alice@corp.example'
const GROUP = 'e2e:readers'

const rbacGroup = 'rbac.authorization.k8s.io'
const role = (kind: 'ClusterRole' | 'Role', name: string, rules: unknown[], namespace?: string): KubeObject => ({ apiVersion: `${rbacGroup}/v1`, kind, metadata: { name, ...(namespace ? { namespace } : {}), labels: { e2e: 'rbac' } }, rules })
const binding = (kind: 'ClusterRoleBinding' | 'RoleBinding', name: string, roleRef: [string, string], subjects: unknown[], namespace?: string): KubeObject => ({
  apiVersion: `${rbacGroup}/v1`,
  kind,
  metadata: { name, ...(namespace ? { namespace } : {}), labels: { e2e: 'rbac' } },
  roleRef: { apiGroup: rbacGroup, kind: roleRef[0], name: roleRef[1] },
  subjects,
})
const user = (name: string) => ({ apiGroup: rbacGroup, kind: 'User', name })
const group = (name: string) => ({ apiGroup: rbacGroup, kind: 'Group', name })
const sa = (namespace: string, name: string) => ({ kind: 'ServiceAccount', namespace, name })

const FIXTURES: KubeObject[] = [
  { apiVersion: 'v1', kind: 'ServiceAccount', metadata: { name: 'e2e-bot', namespace: NS1, labels: { e2e: 'rbac' } } },
  role('ClusterRole', 'e2e-pod-reader', [
    { apiGroups: [''], resources: ['pods'], verbs: ['get', 'list', 'watch'] },
    { apiGroups: [''], resources: ['pods/log'], verbs: ['get'] },
  ]),
  role('ClusterRole', 'e2e-secret-named', [{ apiGroups: [''], resources: ['secrets'], verbs: ['get'], resourceNames: ['e2e-only'] }]),
  role(
    'Role',
    'e2e-deployer',
    [
      { apiGroups: ['apps'], resources: ['deployments'], verbs: ['create', 'update', 'patch'] },
      { apiGroups: [''], resources: ['pods/exec'], verbs: ['create'] },
    ],
    NS1,
  ),
  binding('ClusterRoleBinding', 'e2e-readers', ['ClusterRole', 'e2e-pod-reader'], [group(GROUP)]),
  binding('RoleBinding', 'e2e-deployer', ['Role', 'e2e-deployer'], [sa(NS1, 'e2e-bot'), user(ALICE)], NS1),
  binding('RoleBinding', 'e2e-secret', ['ClusterRole', 'e2e-secret-named'], [group(GROUP)], NS2),
  binding('RoleBinding', 'e2e-alice-edit', ['ClusterRole', 'edit'], [user(ALICE)], NS2),
  binding('RoleBinding', 'e2e-ghost', ['ClusterRole', 'view'], [sa(NS1, 'e2e-ghost')], NS1),
]

type Who = { kind: 'User' | 'Group' | 'ServiceAccount'; name: string; namespace?: string }
const alice: Who = { kind: 'User', name: ALICE }
const readers: Who = { kind: 'Group', name: GROUP }
const bot: Who = { kind: 'ServiceAccount', name: 'e2e-bot', namespace: NS1 }

/** [who, verb, resource (as the matrix names it), namespace or * for all, what kubectl answers] */
const CASES: [Who, string, string, string, boolean][] = [
  [readers, 'get', 'pods', '*', true],
  [readers, 'list', 'pods', NS1, true],
  [readers, 'get', 'pods/log', NS2, true],
  [readers, 'delete', 'pods', NS1, false],
  [readers, 'get', 'secrets', NS2, false],
  [readers, 'create', 'deployments', NS1, false],
  [readers, 'get', 'nodes', '*', false],
  [alice, 'create', 'deployments', NS1, true],
  [alice, 'patch', 'deployments', NS1, true],
  [alice, 'delete', 'deployments', NS1, false],
  [alice, 'create', 'pods/exec', NS1, true],
  [alice, 'create', 'deployments', NS2, true],
  [alice, 'get', 'secrets', NS2, true],
  [alice, 'get', 'secrets', NS1, false],
  [alice, 'list', 'pods', '*', false],
  [alice, 'delete', 'nodes', '*', false],
  [bot, 'update', 'deployments', NS1, true],
  [bot, 'get', 'pods', NS1, false],
  [bot, 'create', 'pods/exec', NS1, true],
  [bot, 'create', 'deployments', NS2, false],
  [bot, 'create', 'rolebindings', NS1, false],
]

function keyOf(w: Who): string {
  return w.kind === 'ServiceAccount' ? `ServiceAccount:${w.namespace}/${w.name}` : `${w.kind}:${w.name}`
}

function canIArgs(w: Who, verb: string, res: string, ns: string): string[] {
  const [resource, sub] = res.split('/') as [string, string?]
  const clusterScoped = ['nodes', 'namespaces', 'persistentvolumes', 'storageclasses', 'clusterroles', 'clusterrolebindings', 'customresourcedefinitions'].includes(resource)
  const as = w.kind === 'ServiceAccount' ? [`--as=system:serviceaccount:${w.namespace}:${w.name}`] : w.kind === 'User' ? [`--as=${w.name}`] : ['--as=e2e-probe', `--as-group=${w.name}`]
  return [verb, resource, ...(sub ? [`--subresource=${sub}`] : []), ...(clusterScoped ? [] : ns === '*' ? ['-A'] : ['-n', ns]), ...as]
}

async function openAccess(page: Page, params = '') {
  await page.goto('/#/access' + params)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

let cleanup: (() => Promise<void>) | null = null

test.beforeAll(async () => {
  cleanup = await cluster.applyObjects(FIXTURES)
})

test.afterAll(async () => {
  await cleanup?.()
})

test('effective permissions match kubectl auth can-i on 21 cases', async ({ page }) => {
  let checked = 0
  for (const [who, verb, res, ns, expected] of CASES) {
    const want = inKind ? await kubectlCanI(...canIArgs(who, verb, res, ns)) : expected
    if (inKind) expect(want, `kubectl auth can-i ${canIArgs(who, verb, res, ns).join(' ')} (the fixture's intent)`).toBe(expected)
    await openAccess(page, `?id=${encodeURIComponent(keyOf(who))}${ns === '*' ? '' : '&scope=' + encodeURIComponent(ns)}`)
    // The identity's bindings are in before any answer is read: a "no" must not be the empty page's.
    await expect(page.getByTestId('identity-grants').locator('[data-grant]').filter({ hasText: /e2e-/ }).first()).toBeVisible({ timeout: 20_000 })
    const cell = page.getByTestId('identity-matrix').locator(`[data-verb="${verb}"][data-res="${res}"]`)
    // In every namespace in scope means yes; some namespaces or named objects only, no.
    await expect(cell, `${keyOf(who)} ${verb} ${res} in ${ns}`).toHaveAttribute('data-level', want ? 'all' : /^(none|some|named)$/, { timeout: 20_000 })
    checked++
  }
  expect(checked).toBe(CASES.length)
})

test('flags what is risky and says who can do it', async ({ page }) => {
  await openAccess(page)
  const review = page.getByTestId('access-review')
  // A person named directly, and a ServiceAccount that does not exist.
  await expect(review.getByTestId('finding').filter({ hasText: `${ALICE} has edit in ${NS2} directly` })).toHaveAttribute('data-sev', 'low', { timeout: 20_000 })
  await expect(review.getByTestId('finding').filter({ hasText: `RoleBinding ${NS1}/e2e-ghost names a ServiceAccount that doesn’t exist` })).toHaveAttribute('data-sev', 'medium')
  const aside = page.getByTestId('access-aside')
  await expect(aside.locator(`[data-identity="ServiceAccount:${NS1}/e2e-ghost"]`)).toContainText('Medium')
  await expect(aside.locator(`[data-identity="User:${ALICE}"]`)).toContainText(`e2e-deployer in ${NS1}`)

  // Who can create deployments in NS1: alice and the bot, through e2e-deployer.
  await page.getByTestId('who-verb').selectOption('create')
  await page.getByTestId('who-res').selectOption('deployments')
  await page.getByTestId('who-ns').selectOption(NS1)
  const who = page.getByTestId('access-who')
  for (const k of [keyOf(alice), keyOf(bot)]) {
    const row = who.locator(`[data-who="${k}"]`)
    await expect(row).toBeVisible()
    await expect(row).toContainText(`${NS1}/e2e-deployer → e2e-deployer`)
  }
  await expect(who.locator(`[data-who="${keyOf(readers)}"]`)).toHaveCount(0)
  // Objects named only: listed, but not counted as can.
  await page.getByTestId('who-verb').selectOption('get')
  await page.getByTestId('who-res').selectOption('secrets')
  await page.getByTestId('who-ns').selectOption(NS2)
  await expect(who.locator(`[data-who="${keyOf(readers)}"]`).getByTestId('who-where')).toHaveText('only e2e-only')

  // The bindings table opens a binding's drawer, which leads to its role.
  await page.getByTestId('tbl-filter').fill('e2e-deployer')
  await page.locator('[data-binding="binding:RoleBinding/' + NS1 + '/e2e-deployer"]').click()
  const panel = page.getByTestId('rbac-panel')
  await expect(panel).toContainText('e2e-deployer')
  await expect(panel.getByTestId('rbac-subject')).toHaveCount(2)
  await panel.getByTestId('rbac-open-role').click()
  await expect(panel.getByTestId('rbac-rule')).toHaveCount(2)
  await expect(panel).toContainText('deployments')
})

test('an identity page says what it has and through which bindings', async ({ page }) => {
  await openAccess(page, `?id=${encodeURIComponent(keyOf(bot))}`)
  await expect(page.getByTestId('identity-name')).toContainText('e2e-bot')
  await expect(page.getByTestId('identity-summary')).toHaveText(`Has e2e-deployer in ${NS1}.`)
  const grants = page.getByTestId('identity-grants')
  await expect(grants.locator('[data-grant]').filter({ hasText: 'e2e-deployer' })).toContainText('names it directly')
  await openAccess(page, `?id=${encodeURIComponent(`ServiceAccount:${NS1}/e2e-ghost`)}`)
  await expect(page.getByTestId('identity-missing')).toBeVisible()
})
