// The test and expect every spec uses. Navigation waits for the document,
// not for the window's load event: every spec then waits for what it
// needs on screen (the connection state, rows), which is what matters.
// Firefox has been seen holding the load event back on a page that opens
// a stream while loading, which turned sound tests into timeouts.
import { test as base, expect } from '@playwright/test'

export const test = base.extend({
  page: async ({ page }, run) => {
    const goto = page.goto.bind(page)
    page.goto = (url, options) => goto(url, { waitUntil: 'domcontentloaded', ...options })
    const reload = page.reload.bind(page)
    page.reload = (options) => reload({ waitUntil: 'domcontentloaded', ...options })
    // Column choices are kept with the signed-in account, so every test
    // starts from the defaults, as it would in a fresh browser.
    await page.request.put('/api/v1/prefs', { data: { prefs: {} } }).catch(() => undefined)
    await run(page)
  },
})

export { expect }
