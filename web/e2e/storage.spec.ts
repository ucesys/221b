// The Storage tab (ST-01 to ST-25): the flat table and its filter sidebar
// by namespace, node and class (ST-02), Needs attention, the claim and
// class drawers, and what a person bound to some namespaces sees. Against
// the simulator the mockups' scenario is there already (ST-23); on the
// kind cluster (ST-24) the test makes a bound claim with the local-path
// provisioner, a claim naming a class that does not exist and a Released
// volume.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { cluster, inKind, kubectl, kubectlRetry, sleep, snapshot, type KubeObject } from './cluster'
import { E2E_DEV, signInAs } from './accounts'
import type { StorageRow } from '../src/api/storage'

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

/** The entries of a sidebar section, and which are ticked. */
async function entries(page: Page, list: 'ns' | 'node' | 'class'): Promise<{ key: string; on: boolean; n: number }[]> {
  return page.getByTestId(`storage-${list}-list`).locator('[data-key]').evaluateAll((rows) =>
    rows.map((r) => ({ key: r.getAttribute('data-key') ?? '', on: r.getAttribute('data-sel') === 'true', n: Number(r.querySelector('.rk-num')?.textContent ?? '0') })),
  )
}

const sorted = async (page: Page) => (await rowKeys(page)).sort()

test.describe('the simulated scenario', () => {
  test.skip(inKind, 'the mockups’ scenario is the simulator’s')

  test('one flat table, by claim, that Needs attention and search narrow (ST-02, ST-04, ST-09, ST-10)', async ({ page }) => {
    await open(page, '#/storage')
    await expect(page.getByTestId('storage-heading')).toContainText('longhorn')
    await expect(page.locator('[data-view], tr[data-group]')).toHaveCount(0)
    await expect.poll(async () => (await rowKeys(page)).length).toBe(25)
    const all = await rowKeys(page)
    // Claim ascending: a namespace's claims sit together, volumes no claim holds last.
    const claimed = all.filter((k) => k.startsWith('pvc:'))
    expect(claimed).toEqual([...claimed].sort((a, b) => a.localeCompare(b, undefined, { numeric: true })))
    expect(all.slice(claimed.length).every((k) => k.startsWith('pv:'))).toBe(true)
    await expect(page.getByTestId('storage-scope')).toHaveText('25 rows · all namespaces · all nodes · all classes')

    const count = Number(await page.getByTestId('storage-attention-count').innerText())
    expect(count).toBeGreaterThan(0)
    await page.getByTestId('storage-attention').check()
    await expect(page).toHaveURL(/attention=1/)
    await expect(page.getByTestId('storage-row')).toHaveCount(count)
    const needing = await rowKeys(page)
    for (const k of ['pvc:analytics/data-cache-2', 'pvc:batch/scratch-report', 'pv:pvc-6d2b8e95-1f4a-4c70-a8d3-e5b9c2f6a187', 'pvc:monitoring/prometheus-db-prometheus-0']) expect(needing).toContain(k)
    await expect(page.locator('[data-testid=storage-row]:not([data-attention])')).toHaveCount(0)
    await page.getByTestId('storage-attention').uncheck()

    await page.getByTestId('storage-search').fill('postgres')
    await expect.poll(() => sorted(page)).toEqual(['pvc:storage/data-postgres-0', 'pvc:storage/data-postgres-1'])
  })

  test('the sidebar filters by namespace, node and class, keeps the selection in the URL, and its counts follow the other filters (ST-02)', async ({ page }) => {
    await open(page, '#/storage')
    const ns = await entries(page, 'ns')
    expect(ns[0]!.key).toBe('(no-claim)')
    expect(ns.map((e) => e.key)).toEqual(expect.arrayContaining(['analytics', 'batch', 'ingest', 'monitoring', 'storage', 'web']))
    // Every namespace, as on the Pods tab: one without claims is there with none.
    expect(ns.find((e) => e.key === 'default')).toMatchObject({ n: 0 })
    const nodes = await entries(page, 'node')
    expect(nodes[0]!.key).toBe('(no-node)')
    expect(nodes.map((e) => e.key)).toContain('worker-002')
    expect((await entries(page, 'class')).map((e) => e.key)).toEqual(['longhorn', 'local-nvme', 'nfs-shared', '(no-class)', 'fast-ssd'])
    await expect(page.getByTestId('storage-ns-list').locator('[data-key=analytics]')).toContainText('quota 4.0Ti of 8.0Ti · 50%')
    await expect(page.getByTestId('storage-class-list').locator('[data-key=fast-ssd]')).toContainText('not found')

    // only: one namespace.
    await page.getByTestId('storage-ns-list').locator('[data-key=analytics]').getByRole('button', { name: 'only' }).click()
    await expect(page).toHaveURL(/ns=analytics/)
    await expect.poll(async () => (await rowKeys(page)).every((k) => k.startsWith('pvc:analytics/'))).toBe(true)
    await expect(page.getByTestId('storage-scope')).toContainText('· analytics · all nodes · all classes')
    // Select all brings back the default.
    await page.getByTestId('storage-ns-list-all').check()
    await expect(page).not.toHaveURL(/ns=/)

    // The selection comes back from the URL; the counts follow the other sections.
    await open(page, '#/storage?ns=analytics&node=(no-node)')
    await expect.poll(() => sorted(page)).toEqual(['pvc:analytics/data-cache-2', 'pvc:analytics/spill-dremio-executor-3'])
    const ns2 = await entries(page, 'ns')
    expect(ns2.filter((e) => e.on).map((e) => e.key)).toEqual(['analytics'])
    expect(ns2.find((e) => e.key === 'analytics')!.n).toBe(2)
    expect((await entries(page, 'node')).filter((e) => e.on).map((e) => e.key)).toEqual(['(no-node)'])
    await expect(page.getByTestId('storage-scope')).toContainText('2 rows · analytics · not on a node · all classes')

    // One node: an RWX claim mounted on several nodes is under each of them.
    await open(page, '#/storage?node=worker-002')
    const onTwo = await rowKeys(page)
    for (const k of ['pvc:analytics/data-cache-0', 'pvc:ingest/buffer-nifi-1', 'pvc:web/media']) expect(onTwo).toContain(k)
    expect(onTwo).not.toContain('pvc:storage/data-postgres-0')
    await open(page, '#/storage?node=worker-004')
    expect(await rowKeys(page)).toContain('pvc:web/media')

    // One class, and the entries for the rows with none.
    await open(page, '#/storage?class=nfs-shared')
    await expect.poll(() => sorted(page)).toEqual(['pv:pvc-6d2b8e95-1f4a-4c70-a8d3-e5b9c2f6a187', 'pvc:analytics/shared-datasets', 'pvc:batch/reports-out', 'pvc:web/media'])
    await open(page, '#/storage?ns=(no-claim)')
    await expect.poll(() => sorted(page)).toEqual(['pv:archive-tapes-2024', 'pv:local-pv-w003-nvme1', 'pv:pvc-6d2b8e95-1f4a-4c70-a8d3-e5b9c2f6a187'])
    await open(page, '#/storage?class=(no-class)')
    await expect.poll(() => sorted(page)).toEqual(['pv:archive-tapes-2024', 'pvc:ingest/hdfs-export'])
    await open(page, '#/storage?node=(no-node)')
    for (const k of await rowKeys(page)) expect(k === 'pv:archive-tapes-2024' || k === 'pv:local-pv-w003-nvme1' || k.includes('pvc-6d2b') || /data-cache-2|spill-dremio-executor-3|scratch-report/.test(k), k).toBe(true)
    // Nothing selected: no row.
    await open(page, '#/storage?class=-')
    await expect(page.getByTestId('storage-row')).toHaveCount(0)
    await expect(page.getByTestId('storage-empty')).toBeVisible()
  })

  test('a claim’s drawer shows the chain and where it breaks (ST-12 to ST-15)', async ({ page }) => {
    await open(page, '#/storage')
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

  test('a class’s drawer says how it provisions and which claims fail, and links to its rows (ST-17, ST-21)', async ({ page }) => {
    await open(page, '#/storage')
    await page.locator('[data-row="pvc:analytics/data-cache-0"] a[data-peek="class:longhorn"]').click()
    const panel = page.getByTestId('storage-class-panel')
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('driver.longhorn.io')
    await expect(panel).toContainText('default')
    await expect(panel.getByTestId('storage-class-failing')).toContainText('analytics/data-cache-2')
    await panel.locator('[data-dtab=parameters]').click()
    await expect(panel).toContainText('numberOfReplicas')
    await panel.locator('[data-dtab=volumes]').click()
    await expect(panel.getByTestId('storage-class-volumes').locator('tbody tr').first()).toBeVisible()
    await panel.getByTestId('storage-class-filter').click()
    await expect(page).toHaveURL(/class=longhorn/)
    await expect(page.getByTestId('storage-class-panel')).toHaveCount(0)
    await expect.poll(async () => (await entries(page, 'class')).filter((e) => e.on).map((e) => e.key)).toEqual(['longhorn'])
    expect(await page.getByTestId('storage-row').count()).toBe(9)
  })

  test('the Namespaces and Nodes tabs link to their rows (ST-21)', async ({ page }) => {
    await page.goto('/#/namespaces/analytics')
    await page.getByTestId('namespace-storage-link').click()
    await expect(page).toHaveURL(/#\/storage\?ns=analytics/)
    await expect.poll(async () => (await rowKeys(page)).every((k) => k.startsWith('pvc:analytics/'))).toBe(true)
    await page.goto('/#/nodes?open=worker-003')
    await page.getByTestId('node-storage-link').click()
    await expect(page).toHaveURL(/#\/storage\?node=worker-003/)
    await expect.poll(async () => (await entries(page, 'node')).filter((e) => e.on).map((e) => e.key)).toEqual(['worker-003'])
  })
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('the Storage tab, its sidebar stacked above the table, and a claim’s drawer fit 390 px', async ({ page }) => {
    const cleanup = await cluster.storageClaim('e2e-storage-phone')
    try {
      await open(page, '#/storage')
      await expect(page.getByTestId('storage-claim').first()).toBeVisible()
      const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
      expect(await overflow()).toBeLessThanOrEqual(0)
      const side = await page.getByTestId('storage-sidebar').boundingBox()
      const table = await page.getByTestId('storage-table').boundingBox()
      expect(side!.y + side!.height).toBeLessThanOrEqual(table!.y)
      await page.getByTestId('storage-claim').first().click()
      const panel = page.getByTestId('storage-panel')
      await expect(panel).toBeVisible()
      await panel.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)))
      expect(await overflow()).toBeLessThanOrEqual(0)
      const box = await panel.boundingBox()
      expect(box!.x + box!.width).toBeLessThanOrEqual(390.5)
    } finally {
      await cleanup()
    }
  })
})

test('a person bound to some namespaces sees only their claims, and their own namespaces, nodes and classes in the sidebar (ST-20)', async ({ page, browser }) => {
  const me = (await (await page.context().request.get('/api/v1/me')).json()) as { mode: string }
  test.skip(me.mode === 'none', 'sign-in is off on this console')
  const allowed = await cluster.limitDev()
  const dev = await signInAs(browser, E2E_DEV)
  try {
    await open(dev, '#/storage')
    await expect(dev.getByTestId('storage-scope-note')).toBeVisible()
    // The simulator's web and analytics hold claims; kind's console namespace may hold none.
    if (!inKind) await expect.poll(async () => (await rowKeys(dev)).length).toBeGreaterThan(0)
    const keys = await rowKeys(dev)
    for (const k of keys) {
      expect(k, 'a cluster-scoped row').toMatch(/^pvc:/)
      expect(allowed, k).toContain(k.slice(4).split('/')[0])
    }
    // The sidebar names nothing the person's own rows do not: no namespace,
    // node or class from elsewhere.
    const pairs = ((await snapshot(dev.context().request, ['storage'])).views.storage?.rows ?? []).flatMap((r) => ((r as StorageRow).pair ? [(r as StorageRow).pair!] : []))
    const theirNodes = new Set(pairs.flatMap((x) => x.nodes ?? []))
    const theirClasses = new Set(pairs.map((x) => x.class))
    for (const e of await entries(dev, 'ns')) expect(allowed, e.key).toContain(e.key)
    for (const e of await entries(dev, 'node')) expect(e.key === '(no-node)' || theirNodes.has(e.key), e.key).toBe(true)
    for (const e of await entries(dev, 'class')) expect(e.key === '(no-class)' || theirClasses.has(e.key), e.key).toBe(true)
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
    await open(page, `#/storage?q=${NS}`)
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

    await page.goto(`/#/storage?peek=claim:${NS}/no-class`)
    const panel = page.getByTestId('storage-panel')
    await expect(panel.getByTestId('storage-chain').locator('[data-hop=class]')).toHaveAttribute('data-state', 'err')
    await page.goto(`/#/storage?peek=claim:${NS}/bound`)
    await expect(panel.getByTestId('storage-chain').locator('[data-hop=pods]')).toContainText('uses-bound')
  })

  test('a viewer bound to one namespace sees nothing from another', async ({ page, browser }) => {
    const me = (await (await page.context().request.get('/api/v1/me')).json()) as { mode: string }
    test.skip(me.mode === 'none', 'sign-in is off on this console')
    await cluster.limitDev()
    const dev = await signInAs(browser, E2E_DEV)
    try {
      await open(dev, '#/storage')
      await expect(dev.locator(`[data-row^="pvc:${NS}/"], [data-row^="pv:"]`)).toHaveCount(0)
      const res = await dev.context().request.get(`/api/v1/storage/claims/${NS}/bound`)
      expect(res.ok()).toBe(false)
    } finally {
      await dev.context().close()
    }
  })
})
