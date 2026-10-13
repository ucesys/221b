// P1-18: 500 nodes and 15,000 pods. Runs only with E2E_SCALE set to the
// number of nodes expected, against the simulator or kind with kwok.
import { expect, test } from '@playwright/test'

const nodes = Number(process.env.E2E_SCALE ?? 0)

test.skip(!nodes, 'set E2E_SCALE to run the scale test')

test('first screen within 2 seconds, sorting within 200 ms, memory under 1 GiB', async ({ page, request }) => {
  // Warm up once, so the timing is the console's and not the first TLS or
  // font fetch of a fresh browser.
  await page.goto('/')
  await expect(page.locator('.rk-grid[data-total]')).toHaveAttribute('data-total', String(nodes), { timeout: 60_000 })

  // Times taken in the page, when the rows appear: the first screenful
  // (what a 900 px window shows; rows past the window are not rendered until
  // scrolled to) and then the moment the grid holds every node.
  await page.addInitScript((n) => {
    const w = window as unknown as { __firstScreen?: number; __allRows?: number }
    new MutationObserver(() => {
      const rows = document.querySelectorAll('[data-node]').length
      if (!w.__firstScreen && rows >= 20) w.__firstScreen = performance.now()
      const total = Number(document.querySelector('.rk-grid[data-total]')?.getAttribute('data-total') ?? 0)
      if (!w.__allRows && total >= n && rows >= 20) w.__allRows = performance.now()
    }).observe(document, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-total'] })
  }, nodes)
  const firsts: number[] = []
  const alls: number[] = []
  for (let i = 0; i < 3; i++) {
    await page.goto('/')
    await page.waitForFunction(() => (window as unknown as { __allRows?: number }).__allRows, undefined, { timeout: 30_000 })
    const t = await page.evaluate(() => {
      const w = window as unknown as { __firstScreen: number; __allRows: number }
      return { first: w.__firstScreen, all: w.__allRows }
    })
    firsts.push(t.first)
    alls.push(t.all)
  }
  const median = (xs: number[]) => [...xs].sort((a, b) => a - b)[Math.floor(xs.length / 2)]!
  const firstScreen = median(firsts)
  const allRows = median(alls)

  const sorts: number[] = []
  for (const col of ['cpu', 'memory', 'name', 'pods', 'status', 'name']) {
    sorts.push(
      await page.evaluate(async (c) => {
        const header = document.querySelector<HTMLButtonElement>(`.rk-gh [data-col="${c}"]`)!
        const t0 = performance.now()
        header.click()
        // Until the next frame is painted.
        await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)))
        return performance.now() - t0
      }, col),
    )
  }
  const sortMedian = [...sorts].sort((a, b) => a - b)[Math.floor(sorts.length / 2)]!

  // Scrolling to the end renders the last rows, and the grid stays whole.
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight))
  await expect(page.locator('[data-node]').last()).toBeVisible()
  await expect(page.locator('.rk-grid[data-total]')).toHaveAttribute('data-total', String(nodes))

  const metrics = await (await request.get('/metrics')).text()
  const rss = Number(/^b221_resident_memory_bytes (\S+)$/m.exec(metrics)?.[1] ?? NaN)
  const pods = Number(/^b221_cached_pods (\S+)$/m.exec(metrics)?.[1] ?? NaN)

  const summary = `first screen ${Math.round(firstScreen)} ms, all ${nodes} rows ${Math.round(allRows)} ms; sort median ${Math.round(sortMedian)} ms (max ${Math.round(Math.max(...sorts))} ms); server memory ${Math.round(rss / 2 ** 20)} MiB; ${pods} pods cached`
  test.info().annotations.push({ type: 'scale', description: summary })
  console.log(`SCALE: ${summary}`)

  expect(pods).toBeGreaterThanOrEqual(15_000)
  expect(firstScreen).toBeLessThan(2_000)
  expect(sortMedian).toBeLessThan(200)
  expect(rss).toBeLessThan(2 ** 30)
})
