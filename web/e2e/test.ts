// The test and expect every spec uses. Navigation waits for the document,
// not for the window's load event: every spec then waits for what it
// needs on screen (the connection state, rows), which is what matters.
// Firefox has been seen holding the load event back on a page that opens
// a stream while loading, which turned sound tests into timeouts. That
// holds for every page a spec opens: the test's own, and those its hooks
// and tests open from the browser or a context of their own (an afterAll
// that sat out its timeout in page.goto failed the last test of its file).
import { test as base, expect, type BrowserContext, type Page } from '@playwright/test'

const waiting = new WeakSet<Page>()

/** The page, its goto and reload waiting for the document by default. */
function forDocument(page: Page): Page {
  if (waiting.has(page)) return page
  waiting.add(page)
  const goto = page.goto.bind(page)
  page.goto = (url, options) => goto(url, { waitUntil: 'domcontentloaded', ...options })
  const reload = page.reload.bind(page)
  page.reload = (options) => reload({ waitUntil: 'domcontentloaded', ...options })
  return page
}

/** The context, its new pages waiting for the document. */
function pagesForDocument(context: BrowserContext): BrowserContext {
  const newPage = context.newPage.bind(context)
  context.newPage = async () => forDocument(await newPage())
  return context
}

export const test = base.extend({
  page: async ({ page }, run) => {
    await run(forDocument(page))
  },
  browser: [
    async ({ browser }, run) => {
      const newPage = browser.newPage.bind(browser)
      browser.newPage = async (options) => forDocument(await newPage(options))
      const newContext = browser.newContext.bind(browser)
      browser.newContext = async (options) => pagesForDocument(await newContext(options))
      await run(browser)
    },
    { scope: 'worker' },
  ],
})

export { expect }
