// The Services tab, as P2-05 describes it.
import type { Page } from '@playwright/test'
import { expect, test } from './test'
import { nWord } from '../src/lib/format'
import { cluster, serviceRows, snapshot } from './cluster'

async function open(page: Page, hash = '#/services') {
  await page.goto('/' + hash)
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await expect(page.locator('[data-service]').first()).toBeVisible()
}

test('lists every Service with its type, cluster IP, ports, endpoints and the External columns', async ({ page, request }) => {
  await open(page)
  const services = serviceRows(await snapshot(request, ['services']))
  expect(services.length).toBeGreaterThan(0)
  await expect(page.getByTestId('services-summary')).toHaveText(nWord(services.length, 'Service', 'Services'))
  // Rows are windowed: the grid knows the total, and the screen shows the first of them.
  await expect(page.locator('.rk-grid[data-total]')).toHaveAttribute('data-total', String(services.length))
  expect(await page.locator('[data-service]').count()).toBeGreaterThanOrEqual(Math.min(services.length, 20))
  // The External group heading spans IP, node ports and hostname.
  await expect(page.locator('.rk-ghg')).toContainText('External')
  for (const col of ['externalIP', 'nodePorts', 'hostnames']) await expect(page.locator(`.rk-gh [data-col="${col}"]`)).toHaveAttribute('aria-label', /^External /)

  const svc = services.find((s) => !s.headless && s.total > 0) ?? services[0]!
  const row = page.locator(`[data-service="${svc.key}"]`)
  await expect(row.locator('.rk-cell').nth(1)).toHaveText(svc.namespace)
  // The type is a column of its own (asked at the Phase 2 review).
  await expect(page.locator('.rk-gh [data-col="type"]')).toContainText('Type')
  await expect(row.locator('.rk-cell').nth(2)).toHaveText(svc.headless ? 'Headless' : svc.type)
  await expect(row.locator('.rk-cell').nth(3)).toHaveText(svc.headless ? 'headless' : svc.clusterIP)
  await expect(row.locator('.rk-cell').nth(4)).toHaveText(svc.ports.join(', '))
  await expect(row.locator('.rk-cell').nth(5)).toContainText(svc.status)

  // Namespaces on the left narrow the table.
  const ns = svc.namespace
  await page.getByTestId('service-namespaces').getByRole('link', { name: new RegExp(`^${ns}\\b`) }).click()
  await expect(page).toHaveURL(new RegExp(`ns=${ns}`))
  const inNs = services.filter((s) => s.namespace === ns)
  await expect(page.getByTestId('services-summary')).toHaveText(`${nWord(inNs.length, 'Service', 'Services')} in ${ns}`)
  await expect(page.locator('.rk-grid[data-total]')).toHaveAttribute('data-total', String(inNs.length))
})

test('a LoadBalancer without an external IP shows as pending, with the reason', async ({ page, request }) => {
  const lb = await cluster.pendingLoadBalancer()
  try {
    await open(page, `#/services?ns=${lb.namespace}`)
    const row = page.locator(`[data-service="${lb.namespace}/${lb.name}"]`)
    await expect(row).toBeVisible({ timeout: 15_000 })
    const ip = row.locator('.rk-cell').nth(6)
    await expect(ip).toHaveText('pending')
    await expect(ip.locator('.rk-t')).toHaveAttribute('title', /.+/)
    const reason = await ip.locator('.rk-t').getAttribute('title')
    expect(reason).toMatch(/SyncLoadBalancerFailed|no load-balancer|waiting|controller/i)

    // No summary cards (removed at the Phase 6 review); the type filter finds it.
    await expect(page.getByTestId('service-cards')).toHaveCount(0)
    await page.getByRole('group', { name: 'Services by type' }).locator('[data-type="LoadBalancer"]').click()
    await expect(page).toHaveURL(/type=LoadBalancer/)
    await expect(page.locator('[data-service]')).toHaveCount((await snapshot(request, ['services'])).views.services!.rows.filter((r) => (r as { namespace: string; type: string; headless: boolean }).namespace === lb.namespace && (r as { type: string }).type === 'LoadBalancer').length)

    // Its details say why.
    await row.click()
    const panel = page.getByTestId('service-panel')
    await expect(panel.getByTestId('pending-why')).toContainText(reason!.split(':')[0]!)
    await expect(panel.locator('dt', { hasText: /^External IPs$/ }).locator('+ dd')).toHaveText('pending')
  } finally {
    await lb.cleanup()
  }
})

test('details list the endpoints and link to the pods behind them', async ({ page, request }) => {
  const services = serviceRows(await snapshot(request, ['services']))
  const svc = services.find((s) => s.selector && s.ready > 0 && !s.name.startsWith('e2e-'))!
  await open(page, `#/services?open=${encodeURIComponent(svc.key)}`)
  const panel = page.getByTestId('service-panel')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(svc.name)
  await expect(panel.locator('dt', { hasText: /^DNS name$/ }).locator('+ dd')).toHaveText(`${svc.name}.${svc.namespace}.svc.cluster.local`)
  const endpoints = panel.getByRole('table', { name: 'Endpoints' })
  await expect(endpoints.locator('.rk-gr')).toHaveCount(svc.total)
  const pod = endpoints.getByTestId('endpoint-pod').first()
  await expect(pod).toBeVisible()
  // One pod: its details open over this tab (asked at the Phase 4 review).
  expect(await pod.getAttribute('data-peek')).toMatch(/^pod:/)
  await expect(panel.getByTestId('service-pods-link')).toHaveAttribute('href', new RegExp(`^#/pods\\?ns=${svc.namespace}`))

  // The link opens the pod's details.
  await pod.click()
  await expect(page.getByTestId('pod-panel')).toBeVisible()
  await expect(page.getByTestId('pod-panel').getByTestId('pod-service-link').first()).toHaveText(svc.name)

  await page.keyboard.press('Escape')
  await page.goto(`/#/services?open=${encodeURIComponent(svc.key)}`)
  await panel.locator('[data-dtab="yaml"]').click()
  await expect(panel.getByTestId('yaml')).toContainText('kind: Service')
  await expect(panel.getByTestId('yaml')).toContainText(`clusterIP: ${svc.clusterIP}`)
})

test('a Service an Ingress publishes opens, with the hostnames that publish it', async ({ page, request }) => {
  const svc = serviceRows(await snapshot(request, ['services'])).find((s) => s.hostnames.length > 0)
  test.skip(!svc, 'no Ingress publishes a Service here')
  await open(page, `#/services?open=${encodeURIComponent(svc!.key)}`)
  const panel = page.getByTestId('service-panel')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(svc!.name)
  await expect(panel.getByTestId('service-ingress').first()).toContainText(svc!.hostnames[0]!)
})
