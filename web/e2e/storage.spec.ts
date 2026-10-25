// The Storage tab (ST-01 to ST-25): the three views, the Needs attention
// filter, the claim and class drawers, and what a person bound to some
// namespaces sees. Against the simulator the mockups' scenario is there
// already (ST-23); on the kind cluster (ST-24) the test makes a bound claim
// with the local-path provisioner, a claim naming a class that does not
// exist and a Released volume.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, inKind, kubectl, kubectlRetry, sleep, type KubeObject } from './cluster'
import { E2E_DEV, signInAs } from './accounts'

const BUSYBOX = 'registry.k8s.io/e2e-test-images/busybox:1.36.1-1'

async function open(page: Page, hash: string): Promise<void> {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected', { timeout: 20_000 })
  await expect(page.getByTestId('storage-table')).toBeVisible()
}

/** The keys of the rows on screen, in order. */
async function rowKeys(page: Page): Promise<string[]> {
  return page.getByTestId('storage-row').evaluateAll((rows) => rows.map((r) => r.getAttribute('data-row') ?? ''))
}

async function groupKeys(page: Page): Promise<string[]> {
  return page.locator('tr[data-group]').evaluateAll((rows) => rows.map((r) => r.getAttribute('data-group') ?? ''))
}

test.describe('the simulated scenario', () => {
  test.skip(inKind, 'the mockups’ scenario is the simulator’s')

  test('the three views group the same volumes, and Needs attention narrows them (ST-02, ST-09, ST-11)', async ({ page }) => {
    await open(page, '#/storage?view=class')
    // ST-04: the default class first, the others by name, then no class, then the missing ones.
    await expect.poll(() => groupKeys(page)).toEqual(['class:longhorn', 'class:local-nvme', 'class:nfs-shared', 'none', 'missing:fast-ssd'])
    await expect(page.getByTestId('storage-heading')).toContainText('longhorn')
    const all = await rowKeys(page)
    expect(all).toContain('pvc:analytics/data-cache-2')
    expect(all).toContain('pv:local-pv-w003-nvme1')

    const count = Number(await page.getByTestId('storage-attention-count').innerText())
    expect(count).toBeGreaterThan(0)
    await page.getByTestId('storage-attention').check()
    await expect(page).toHaveURL(/attention=1/)
    await expect(page.getByTestId('storage-row')).toHaveCount(count)
    const needing = await rowKeys(page)
    for (const k of ['pvc:analytics/data-cache-2', 'pvc:batch/scratch-report', 'pv:pvc-6d2b8e95-1f4a-4c70-a8d3-e5b9c2f6a187', 'pvc:monitoring/prometheus-db-prometheus-0']) expect(needing).toContain(k)
    expect(needing).not.toContain('pvc:storage/data-postgres-0')
    await expect(page.locator('[data-testid=storage-row]:not([data-attention])')).toHaveCount(0)
    await page.getByTestId('storage-attention').uncheck()

    await page.locator('[data-view=namespace]').click()
    await expect(page).toHaveURL(/view=namespace/)
    const ns = await groupKeys(page)
    expect(ns).toEqual(expect.arrayContaining(['ns:analytics', 'ns:batch', 'ns:ingest', 'ns:monitoring', 'ns:storage', 'ns:web']))
    expect(ns.at(-1)).toBe('unclaimed')

    await page.locator('[data-view=all]').click()
    await expect(page.locator('tr[data-group]')).toHaveCount(0)
    expect((await rowKeys(page)).sort()).toEqual([...all].sort())

    // The search finds a claim by its name (ST-10).
    await page.getByTestId('storage-search').fill('postgres')
    await expect.poll(async () => (await rowKeys(page)).sort()).toEqual(['pvc:storage/data-postgres-0', 'pvc:storage/data-postgres-1'])
  })

  test('a claim’s drawer shows the chain and where it breaks (ST-12 to ST-15)', async ({ page }) => {
    await open(page, '#/storage?view=all')
    await page.locator('[data-row="pvc:analytics/data-cache-2"] [data-testid=storage-claim]').click()
    const panel = page.getByTestId('storage-panel')
    await expect(panel).toBeVisible()
    await expect(page).toHaveURL(/peek=claim(:|%3A)analytics(\/|%2F)data-cache-2/)
    const chain = panel.getByTestId('storage-chain')
    await expect(chain.locator('[data-hop=class]')).toHaveAttribute('data-state', 'ok')
    await expect(chain.locator('[data-hop=volume]')).toHaveAttribute('data-state', 'err')
    await expect(panel.getByTestId('storage-intro')).toContainText('breaks at the volume')
    await expect(panel.getByTestId('storage-events')).toContainText('ProvisioningFailed')
    await page.keyboard.press('Escape')
    await expect(panel).toHaveCount(0)

    await page.locator('[data-row="pvc:storage/data-postgres-0"] [data-testid=storage-claim]').click()
    await expect(panel).toBeVisible()
    await expect(chain.locator('[data-state=err], [data-state=warn]')).toHaveCount(0)
    await expect(chain.locator('[data-hop=usage]')).toBeVisible()
    await expect(chain.locator('[data-hop=pods]')).toContainText('postgres-0')
    await panel.locator('[data-dtab=pods]').click()
    await expect(panel.getByTestId('storage-pods')).toContainText('postgres-0')
    // No Secret value is in a drawer (ST-20): the planted value never reaches the page.
    expect(await page.content()).not.toContain('PLANTED-SECRET-VALUE')
    await page.keyboard.press('Escape')
  })

  test('a class’s drawer says how it provisions and which claims fail (ST-17)', async ({ page }) => {
    await open(page, '#/storage?view=class')
    await page.locator('[data-group="class:longhorn"] [data-testid=storage-class-link]').click()
    const panel = page.getByTestId('storage-class-panel')
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('driver.longhorn.io')
    await expect(panel).toContainText('default')
    await expect(panel.getByTestId('storage-class-failing')).toContainText('analytics/data-cache-2')
    await panel.locator('[data-dtab=volumes]').click()
    await expect(panel.getByTestId('storage-class-volumes').locator('tbody tr').first()).toBeVisible()
    await panel.locator('[data-dtab=parameters]').click()
    await expect(panel).toContainText('numberOfReplicas')
    await page.keyboard.press('Escape')
  })
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('the Storage tab and a claim’s drawer fit 390 px', async ({ page }) => {
    await open(page, '#/storage?view=namespace')
    const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(await overflow()).toBeLessThanOrEqual(0)
    await page.getByTestId('storage-claim').first().click()
    const panel = page.getByTestId('storage-panel')
    await expect(panel).toBeVisible()
    await panel.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)))
    expect(await overflow()).toBeLessThanOrEqual(0)
    const box = await panel.boundingBox()
    expect(box!.x + box!.width).toBeLessThanOrEqual(390.5)
  })
})

test('a person bound to some namespaces sees only their claims, and no volumes or classes (ST-20)', async ({ page, browser }) => {
  const me = (await (await page.context().request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const allowed = await cluster.limitDev()
  const dev = await signInAs(browser, E2E_DEV)
  try {
    await open(dev, '#/storage')
    await expect(dev.locator('[data-view=class]')).toBeDisabled()
    await expect(dev.getByTestId('storage-scope-note')).toBeVisible()
    // The simulator's web and analytics hold claims; kind's console namespace may hold none.
    if (!inKind) await expect.poll(async () => (await rowKeys(dev)).length).toBeGreaterThan(0)
    const keys = await rowKeys(dev)
    for (const k of keys) {
      expect(k, 'a cluster-scoped row').toMatch(/^pvc:/)
      expect(allowed, k).toContain(k.slice(4).split('/')[0])
    }
    const api = dev.context().request
    for (const path of ['/api/v1/storage/claims/ingest/data-kafka-0', '/api/v1/storage/volumes/pvc-8f2c41d6-5a1e-4c2b-9f0a-3d7e1b6c8a21']) {
      const res = await api.get(path)
      expect(res.ok(), path).toBe(false)
    }
  } finally {
    await dev.context().close()
  }
})

test.describe('on the kind cluster (ST-24)', () => {
  test.skip(!inKind, 'needs a real provisioner')
  const NS = 'e2e-storage'
  const PV = 'e2e-storage-released'
  let cleanup: () => Promise<void> = async () => {}

  test.beforeAll(async () => {
    test.setTimeout(180_000)
    const objs: KubeObject[] = [
      { apiVersion: 'v1', kind: 'Namespace', metadata: { name: NS, labels: { e2e: 'storage' } } },
      // A claim of kind's default class (local-path, WaitForFirstConsumer): bound once its pod is scheduled.
      { apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: { name: 'bound', namespace: NS }, spec: { accessModes: ['ReadWriteOnce'], resources: { requests: { storage: '64Mi' } } } },
      {
        apiVersion: 'v1',
        kind: 'Pod',
        metadata: { name: 'uses-bound', namespace: NS },
        spec: {
          terminationGracePeriodSeconds: 0,
          containers: [{ name: 'main', image: BUSYBOX, command: ['sh', '-c', 'sleep 3600'], volumeMounts: [{ name: 'data', mountPath: '/data' }] }],
          volumes: [{ name: 'data', persistentVolumeClaim: { claimName: 'bound' } }],
        },
      },
      { apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: { name: 'no-class', namespace: NS }, spec: { storageClassName: 'e2e-no-such-class', accessModes: ['ReadWriteOnce'], resources: { requests: { storage: '64Mi' } } } },
      // A volume made by hand, kept when its claim goes: Released once the claim is deleted.
      { apiVersion: 'v1', kind: 'PersistentVolume', metadata: { name: PV, labels: { e2e: 'storage' } }, spec: { storageClassName: '', capacity: { storage: '64Mi' }, accessModes: ['ReadWriteOnce'], persistentVolumeReclaimPolicy: 'Retain', hostPath: { path: '/tmp/e2e-storage-released' } } },
      { apiVersion: 'v1', kind: 'PersistentVolumeClaim', metadata: { name: 'goes', namespace: NS }, spec: { storageClassName: '', volumeName: PV, accessModes: ['ReadWriteOnce'], resources: { requests: { storage: '64Mi' } } } },
    ]
    const remove = await cluster.applyObjects(objs)
    cleanup = async () => {
      await remove()
      await kubectl('delete', 'pv', PV, '--ignore-not-found', '--wait=false')
    }
    const phase = (kind: string, name: string, ns?: string) => kubectlRetry('get', kind, name, ...(ns ? ['-n', ns] : []), '-o', 'jsonpath={.status.phase}')
    for (let i = 0; i < 90 && ((await phase('pvc', 'bound', NS)) !== 'Bound' || (await phase('pvc', 'goes', NS)) !== 'Bound'); i++) await sleep(1000)
    await kubectl('-n', NS, 'delete', 'pvc', 'goes', '--wait=true')
    for (let i = 0; i < 60 && (await phase('pv', PV)) !== 'Released'; i++) await sleep(1000)
    expect(await phase('pv', PV)).toBe('Released')
  })

  test.afterAll(async () => cleanup())

  test('a bound claim, a claim naming a missing class and a Released volume', async ({ page }) => {
    await open(page, `#/storage?view=all&q=${NS}`)
    const bound = page.locator(`[data-row="pvc:${NS}/bound"]`)
    await expect(bound).toContainText('Bound')
    await expect(bound).toContainText('standard')
    const missing = page.locator(`[data-row="pvc:${NS}/no-class"]`)
    await expect(missing).toContainText('Pending')
    await expect(missing).toContainText('e2e-no-such-class')
    await expect(missing).toHaveAttribute('data-attention', 'true')
    await page.getByTestId('storage-search').fill(PV)
    const released = page.locator(`[data-row="pv:${PV}"]`)
    await expect(released).toContainText('Released')
    await expect(released).toHaveAttribute('data-attention', 'true')

    await page.goto(`/#/storage?view=class&peek=claim:${NS}/no-class`)
    const panel = page.getByTestId('storage-panel')
    await expect(panel.getByTestId('storage-chain').locator('[data-hop=class]')).toHaveAttribute('data-state', 'err')
    await page.goto(`/#/storage?view=all&peek=claim:${NS}/bound`)
    await expect(panel.getByTestId('storage-chain').locator('[data-hop=pods]')).toContainText('uses-bound')
  })

  test('a viewer bound to one namespace sees nothing from another', async ({ page, browser }) => {
    const me = (await (await page.context().request.get('/api/v1/me')).json()) as { mode: string }
    test.skip(me.mode === 'none', 'sign-in is off on this console')
    await cluster.limitDev()
    const dev = await signInAs(browser, E2E_DEV)
    try {
      await open(dev, '#/storage?view=all')
      await expect(dev.locator(`[data-row^="pvc:${NS}/"], [data-row^="pv:"]`)).toHaveCount(0)
      const res = await dev.context().request.get(`/api/v1/storage/claims/${NS}/bound`)
      expect(res.ok()).toBe(false)
    } finally {
      await dev.context().close()
    }
  })
})
