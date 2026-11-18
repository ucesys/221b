import type { Page } from '@playwright/test'
import { expect } from './test'

type Api = (page: Page, method: string, path: string, body?: unknown) => Promise<{ status: number; json: unknown }>

// askSherlockFor has Sherlock answer with a command, so a command sent to
// Watson as Sherlock's is one of its answers (only those become proposed
// changes). Sherlock must be on for the cluster; the simulated model
// answers "Suggest the command X" with X.
export async function askSherlockFor(page: Page, api: Api, command: string): Promise<void> {
  const asked = await api(page, 'POST', '/api/v1/sherlock/ask', { question: `Suggest the command ${command}` })
  expect(asked.status, JSON.stringify(asked.json)).toBe(202)
  await expect
    .poll(
      () =>
        page.evaluate(async (cmd) => {
          const ctl = new AbortController()
          const res = await fetch('/api/v1/sherlock/events', { signal: ctl.signal })
          const reader = res.body!.getReader()
          const dec = new TextDecoder()
          const mark = 'event: conversation\ndata: '
          let text = ''
          let data = ''
          for (let i = 0; i < 200; i++) {
            const at = text.indexOf(mark)
            const stop = at >= 0 ? text.indexOf('\n\n', at) : -1
            if (stop >= 0) {
              data = text.slice(at + mark.length, stop)
              break
            }
            const { value, done } = await reader.read()
            if (done) break
            text += dec.decode(value, { stream: true })
          }
          ctl.abort()
          try {
            const v = JSON.parse(data) as { busy?: boolean; messages?: { role?: string; text?: string }[] }
            const last = (v.messages ?? []).at(-1)
            return !v.busy && !!last && last.role !== 'user' && (last.text ?? '').includes(cmd)
          } catch {
            return false
          }
        }, command),
      { timeout: 30_000 },
    )
    .toBe(true)
}
