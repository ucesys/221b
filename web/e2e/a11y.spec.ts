// X-12: WCAG 2.2 AA, checked with axe in both themes, with the details
// panel, the Columns menu and the Settings dialog open as well as closed.
import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { expect, test } from './test'

const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']

async function violations(page: Page): Promise<string[]> {
  // Once the drawers have slid in: axe samples colors as painted. A transition
  // restarted midway (the refresh ring's, every second) rejects, which is fine.
  await page.evaluate(() => Promise.allSettled(document.getAnimations().map((a) => a.finished)))
  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze()
  return results.violations.map((v) => `${v.id} (${v.impact}): ${v.help} — ${v.nodes.slice(0, 3).map((n) => n.target.join(' ')).join(', ')}`)
}

for (const theme of ['light', 'dark'] as const) {
  test(`meets WCAG 2.2 AA in the ${theme} theme`, async ({ page }) => {
    test.slow() // a dozen axe runs, a few seconds each
    await page.addInitScript((t) => window.localStorage.setItem('221b.theme', JSON.stringify(t)), theme)
    await page.goto('/')
    await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
    await expect(page.locator('.rk')).toHaveAttribute('data-theme', theme)
    await expect(page.locator('[data-node]').first()).toBeVisible()
    expect(await violations(page)).toEqual([])

    await page.locator('[data-node] .rk-pname').first().click()
    await expect(page.getByTestId('node-panel')).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: /^Columns/ }).click()
    await expect(page.getByRole('group', { name: 'Columns to show' })).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: /^Export/ }).click()
    await expect(page.getByRole('group', { name: 'Export format' })).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.keyboard.press('Escape')

    // The Phase 2 tabs, and a pod's details with its logs.
    await page.goto('/#/pods')
    await expect(page.locator('[data-pod]').first()).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.locator('[data-pod] .rk-pname').first().click()
    await expect(page.getByTestId('pod-panel')).toBeVisible()
    await expect(page.getByTestId('pod-panel').locator('dt').first()).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.getByTestId('pod-panel').locator('[data-dtab="logs"]').click()
    await expect(page.getByTestId('logs')).toBeVisible()
    expect(await violations(page)).toEqual([])
    await page.keyboard.press('Escape')
    for (const hash of ['#/namespaces', '#/services', '#/helm']) {
      await page.goto('/' + hash)
      await expect(page.locator('.rk-gr').first()).toBeVisible()
      expect(await violations(page), hash).toEqual([])
    }
    // An object's details with the pods it controls (asked at the Phase 2 review).
    await page.goto('/#/namespaces')
    const deployment = page.locator('[data-kind="Deployment"] [data-object]').first()
    await expect(deployment).toBeVisible()
    await deployment.click()
    const panel = page.getByTestId('object-panel')
    await expect(panel.locator('dt').first()).toBeVisible()
    expect(await violations(page), 'object details').toEqual([])
    await panel.locator('[data-dtab="pods"]').click()
    await expect(panel.locator('.rk-gr').first().or(panel.getByText(/^No pod/))).toBeVisible()
    expect(await violations(page), 'object pods').toEqual([])
    await page.keyboard.press('Escape')
  })

  test(`the Phase 4 tabs meet WCAG 2.2 AA in the ${theme} theme`, async ({ page }) => {
    test.slow()
    await page.addInitScript((t) => window.localStorage.setItem('221b.theme', JSON.stringify(t)), theme)
    for (const hash of ['#/fleets', '#/alerts', '#/networks', '#/access', '#/policies', '#/policies/netpol', '#/policies/pdb', '#/policies/quota', '#/policies/psa', '#/policies/webhooks']) {
      await page.goto('/' + hash)
      await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
      await expect(page.locator('.rk-gr, .rk-card').first()).toBeVisible()
      await page.waitForTimeout(500)
      expect(await violations(page), hash).toEqual([])
    }
  })
}

test('every control is reachable by keyboard with a visible focus ring', async ({ page, browserName }) => {
  // Safari's Tab key skips buttons unless the user turns on full keyboard access.
  test.skip(browserName === 'webkit', 'Tab skips buttons in Safari by default')
  await page.goto('/')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const seen = new Set<string>()
  for (let i = 0; i < 25; i++) {
    await page.keyboard.press('Tab')
    const info = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      if (!el || el === document.body) return null
      // Text fields show focus on their frame.
      const style = getComputedStyle(el.closest('.rk-field') ?? el)
      const ring = (style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) > 0) || style.boxShadow !== 'none'
      return { name: el.getAttribute('aria-label') ?? el.textContent?.trim().slice(0, 40) ?? el.tagName, ring }
    })
    if (!info) continue
    expect(info.ring, `focus ring on ${info.name}`).toBe(true)
    seen.add(info.name)
  }
  // The settings button, the role filter, the search box, the column menu,
  // the export, the column headers and the first rows all take focus.
  expect(seen.size).toBeGreaterThan(15)
  expect(seen.has('Settings')).toBe(true)
})
