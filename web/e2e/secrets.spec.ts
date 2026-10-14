// P2-07 and X-01: a Secret value planted in the cluster appears in no
// response, no export and no log line of the console.
import { readFileSync } from 'node:fs'
import { expect, test } from './test'
import type { NamespaceRow, PodRow } from '../src/api/types'
import { cluster, inKind, PLANTED, PLANTED_HELM, snapshot } from './cluster'

test('the planted Secret value reaches no response, export or log', async ({ page, request }) => {
  test.setTimeout(180_000)
  const secret = await cluster.plantedSecret()
  const planted = [PLANTED, Buffer.from(PLANTED).toString('base64'), ...(inKind ? [] : [PLANTED_HELM])]
  const seen: string[] = []
  const check = (what: string, text: string) => {
    for (const p of planted) if (text.includes(p)) seen.push(`${what} contains ${p.slice(0, 16)}…`)
  }
  try {
    // Every view the browser can subscribe to, including the namespace the Secret is in.
    const namespaces = (await snapshot(request, ['namespaces'])).views.namespaces!.rows as NamespaceRow[]
    const views = ['nodes', 'pods', 'namespaces', 'services', 'helm', ...namespaces.map((n) => `ns:${n.name}`)]
    for (let i = 0; i < views.length; i += 8) {
      const res = await request.get(`/api/v1/snapshot?views=${views.slice(i, i + 8).join(',')}`)
      expect(res.ok()).toBe(true)
      check(`snapshot of ${views.slice(i, i + 8).join(',')}`, await res.text())
    }
    // Every pod's details and YAML in the Secret's namespace, and every Secret's YAML.
    const pods = (await snapshot(request, ['pods'])).views.pods!.rows as PodRow[]
    for (const p of pods.filter((x) => x.namespace === secret.namespace).slice(0, 30)) {
      for (const path of [`/api/v1/pods/${p.namespace}/${p.name}`, `/api/v1/yaml/pods/${p.namespace}/${p.name}`]) check(path, await (await request.get(path)).text())
    }
    check('secret yaml', await (await request.get(`/api/v1/yaml/secrets/${secret.namespace}/${secret.name}`)).text())
    check('secret yaml download', await (await request.get(`/api/v1/yaml/secrets/${secret.namespace}/${secret.name}?download=1`)).text())
    // Every Service's details in the namespace, and every Helm release with its values.
    const services = (await snapshot(request, ['services'])).views.services!.rows as { namespace: string; name: string }[]
    for (const svc of services.filter((x) => x.namespace === secret.namespace)) check(`service ${svc.name}`, await (await request.get(`/api/v1/services/${svc.namespace}/${svc.name}`)).text())
    const releases = (await snapshot(request, ['helm'])).views.helm!.rows as { namespace: string; name: string }[]
    for (const r of releases) check(`release ${r.name}`, await (await request.get(`/api/v1/helm/${r.namespace}/${r.name}`)).text())
    // The event stream, for a few refreshes.
    const stream = await request.get(`/api/v1/events?views=nodes,pods,namespaces,services,helm,ns:${secret.namespace}`, { timeout: 8_000 }).catch(() => null)
    if (stream) check('event stream', await stream.text().catch(() => ''))

    // Exports from the browser: the namespace's objects and the pods, as JSON and CSV.
    await page.goto(`/#/namespaces/${secret.namespace}`)
    await expect(page.getByTestId('namespace-name')).toHaveText(secret.namespace)
    await expect(page.locator(`[data-object="Secret/${secret.name}"]`)).toBeVisible({ timeout: 15_000 })
    // Excel is XML inside an uncompressed ZIP, so the bytes are searchable as text.
    for (const format of ['JSON', 'CSV', 'Excel']) {
      await page.getByRole('button', { name: /^Export/ }).first().click()
      const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('group', { name: 'Export format' }).getByRole('button', { name: format }).click()])
      check(`namespace export ${format}`, readFileSync(await download.path(), 'latin1'))
    }
    await page.goto(`/#/pods?ns=${secret.namespace}`)
    await expect(page.locator('[data-pod]').first()).toBeVisible()
    await page.getByRole('button', { name: /^Columns/ }).click()
    for (const name of ['Labels', 'Owner']) await page.getByRole('checkbox', { name }).check()
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: /^Export/ }).click()
    const [podsJson] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'JSON' }).click()])
    check('pods export', readFileSync(await podsJson.path(), 'utf8'))

    // The pod details on screen, environment included.
    const target = pods.find((p) => p.namespace === secret.namespace && p.status === 'Running')
    if (target) {
      await page.goto(`/#/pods?open=${encodeURIComponent(target.key)}`)
      const panel = page.getByTestId('pod-panel')
      await expect(panel.getByRole('heading', { level: 2 })).toHaveText(target.name)
      for (const tab of ['environment', 'volumes', 'yaml']) {
        await panel.locator(`[data-dtab="${tab}"]`).click()
        await expect(panel.locator('[data-dtab]').first()).toBeVisible()
        check(`pod ${tab} on screen`, await panel.innerText())
      }
    }

    // The server's own log.
    check('server log', await cluster.serverLog())
  } finally {
    await secret.cleanup()
  }
  expect(seen).toEqual([])
})
