// Alerts and cases (Phase 12, wave 6, CF-77): an alert an open case
// already covers shows that case's C-n in place of New Case, and the link
// opens the case's drawer. New Case on a crash-looping pod opens a case
// about it; back on the Alerts tab the alert names that case. Since CF-84
// the case names the alert by its reason and links it on the Alerts tab,
// saying it is still going.
import type { APIRequestContext, Page } from '@playwright/test'
import { expect, test } from './test'
import type { AlertCovers } from '../src/api/casework'
import { cluster, consoleNode, inKind, podCrashing, podRows, snapshot } from './cluster'

const SAME = { 'Sec-Fetch-Site': 'same-origin' }
// The console's own cluster, as the Alerts tab's route names it.
const LOCAL = 'local'

async function open(page: Page) {
  await page.goto('/#/alerts')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
}

/** Closes the open cases that cover an alert, so it starts with none. */
async function closeCovering(request: APIRequestContext, key: string, object: string) {
  const res = await request.get(`/api/v1/cases/alert-cases?cluster=${LOCAL}`)
  expect(res.status(), await res.text()).toBe(200)
  const covers = (await res.json()) as AlertCovers
  const ids = new Set([...(covers.alerts[key] ?? []), ...(covers.objects[object.toLowerCase()] ?? [])].map((c) => c.id))
  for (const id of ids) await request.post(`/api/v1/cases/cases/${id}/close`, { headers: SAME, data: { outcome: 'noFixNeeded', explanation: 'e2e: done' } })
}

test('an alert an open case covers shows the case in place of New Case, and opens it (CF-77)', async ({ page, request }) => {
  test.setTimeout(inKind ? 300_000 : 90_000)
  let pod: string
  let cleanup = async () => {}
  if (inKind) {
    const crash = await cluster.crashLoop(await consoleNode())
    pod = crash.pod
    cleanup = crash.cleanup
    await expect.poll(() => podCrashing(pod), { timeout: 120_000, intervals: [1000] }).toBe(true)
  } else {
    pod = podRows(await snapshot(request, ['pods'])).find((p) => p.status === 'CrashLoopBackOff')!.key
  }
  const [ns, name] = pod.split('/') as [string, string]
  const key = `pod:${pod}`
  const object = `${ns}/Pod/${name}`
  let opened = ''
  try {
    await closeCovering(request, key, object)
    await open(page)
    const card = page.locator(`[data-alert="${key}"]`)
    await expect(card).toHaveAttribute('data-severity', 'failed', { timeout: 60_000 })
    // No open case covers it: New Case.
    await expect(card.getByTestId('alert-new-case')).toBeVisible()
    await expect(card.getByTestId('alert-case-link')).toHaveCount(0)
    await card.getByTestId('alert-new-case').click()
    await expect(page).toHaveURL(/#\/cases\/inbox\/case\/\d+/)
    opened = `C-${/case\/(\d+)/.exec(page.url())![1]}`
    // The case names the alert by its reason (a crash loop's status flips
    // to Error between restarts), links it on its cluster's
    // Alerts tab filtered to the pod, and says it is still going (CF-84).
    await expect(page.getByTestId('case-alert-name')).toHaveText(/CrashLoopBackOff|Error/)
    await expect(page.getByTestId('case-alert-link')).toHaveAttribute('href', `#/alerts?q=${name}`)
    await expect(page.getByTestId('case-alert-state')).toHaveText('still going', { timeout: 15_000 })

    // Back on the Alerts tab, the alert names its case instead.
    await open(page)
    const link = card.getByTestId('alert-case-link')
    await expect(link).toHaveText(opened, { timeout: 15_000 })
    await expect(link).toHaveAttribute('data-case', opened)
    await expect(card.getByTestId('alert-new-case')).toHaveCount(0)
    await link.click()
    await expect(page).toHaveURL(new RegExp(`#/cases/inbox/case/${opened.slice(2)}$`))

    // Closed, the case no longer covers it: New Case again.
    await closeCovering(request, key, object)
    opened = ''
    await open(page)
    await expect(card.getByTestId('alert-new-case')).toBeVisible({ timeout: 15_000 })
  } finally {
    if (opened) await request.post(`/api/v1/cases/cases/${opened}/close`, { headers: SAME, data: { outcome: 'noFixNeeded', explanation: 'e2e: done' } })
    await cleanup()
  }
})
