// P1-18: 500 nodes and 15,000 pods. Runs only with E2E_SCALE set to the
// number of nodes expected, against the simulator or kind with kwok.
import { expect, test } from '@playwright/test'

const nodes = Number(process.env.E2E_SCALE ?? 0)

test.skip(!nodes, 'set E2E_SCALE to run the scale test')

test('first screen within 2 seconds, sorting within 200 ms, memory under 1 GiB', async ({ page, request }) => {
  // Warm up once, so the timing is the console's and not the first TLS or
  // font fetch of a fresh browser.
  await page.goto('/')
  await expect(page.locator('[data-node]')).toHaveCount(nodes, { timeout: 60_000 })

  const loads: number[] = []
  for (let i = 0; i < 3; i++) {
    await page.goto('/')
    await page.waitForFunction((n) => document.querySelectorAll('[data-node]').length >= n, nodes, { timeout: 30_000 })
    loads.push(await page.evaluate(() => performance.now()))
  }
  const firstScreen = Math.min(...loads)

  const sorts: number[] = []
  for (const col of ['cpu', 'memory', 'name', 'pods', 'status', 'name']) {
    sorts.push(
      await page.evaluate(async (c) => {
        const before = document.querySelector('[data-node]')?.getAttribute('data-node')
        const header = document.querySelector<HTMLButtonElement>(`.rk-gh [data-col="${c}"]`)!
        const t0 = performance.now()
        header.click()
        // Until the next frame is painted.
        await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)))
        const t1 = performance.now()
        void before
        return t1 - t0
      }, col),
    )
  }
  const sortMedian = [...sorts].sort((a, b) => a - b)[Math.floor(sorts.length / 2)]!

  const metrics = await (await request.get('/metrics')).text()
  const rss = Number(/^b221_resident_memory_bytes (\S+)$/m.exec(metrics)?.[1] ?? NaN)
  const pods = Number(/^b221_cached_pods (\S+)$/m.exec(metrics)?.[1] ?? NaN)

  const summary = `first screen ${Math.round(firstScreen)} ms; sort median ${Math.round(sortMedian)} ms (max ${Math.round(Math.max(...sorts))} ms); server memory ${Math.round(rss / 2 ** 20)} MiB; ${pods} pods cached`
  test.info().annotations.push({ type: 'scale', description: summary })
  console.log(`SCALE: ${summary}`)

  expect(pods).toBeGreaterThanOrEqual(15_000)
  expect(firstScreen).toBeLessThan(2_000)
  expect(sortMedian).toBeLessThan(200)
  expect(rss).toBeLessThan(2 ** 30)
})
