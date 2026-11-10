// A wiki draft's diff (KN-08): its lines wrap, so a page's long paragraph
// never widens the page, and its front matter is folded into one line.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { DraftRun } from '../../api/knowledge'
import { DraftRunCard, Diff, frontMatter } from './KnowledgeDraft'

const diff = ['--- pages/system/rke2.md', '+++ pages/system/rke2.md', '@@ -1 +1,9 @@', '+---', '+kind: system', '+sources:', '+- title: Quick Start | RKE2', '+title: RKE2', '+---', '+# RKE2', '+' + 'A long paragraph of the page. '.repeat(40)].join('\n')

afterEach(cleanup)

describe('a draft page’s diff', () => {
  it('folds the front matter into one line that opens', () => {
    render(<Diff text={diff} />)
    const fold = screen.getByTestId('knowledge-diff-meta')
    expect(fold.textContent).toBe('Front matter: 6 lines (sources, dates)')
    expect(screen.getByTestId('knowledge-diff').textContent).not.toContain('kind: system')
    expect(screen.getByTestId('knowledge-diff').textContent).toContain('# RKE2')
    fireEvent.click(fold)
    expect(screen.getByTestId('knowledge-diff').textContent).toContain('kind: system')
  })

  it('wraps its lines rather than widening the page', () => {
    render(<Diff text={diff} />)
    const lines = screen.getByTestId('knowledge-diff').querySelectorAll('div')
    expect([...lines].every((l) => l.style.whiteSpace === 'pre-wrap')).toBe(true)
  })

  it('finds front matter only where a page starts with it', () => {
    expect(frontMatter(diff.split('\n'))).toEqual({ start: 3, end: 8 })
    expect(frontMatter(['--- a', '+++ b', '@@ -1 +1 @@', '+# Title', '+---', '+text', '+more', '+---'])).toBeNull()
    expect(frontMatter(['+---', '+a: 1', '+---'])).toBeNull()
  })
})

describe('a draft that contradicts a fix (KN-33)', () => {
  const run: DraftRun = {
    id: 'r-1',
    started: '2026-11-09T10:00:00Z',
    by: 'run-4, Housekeeping',
    state: 'waiting',
    store: 'git',
    pages: [
      {
        path: 'pages/runbook/disk-pressure.md',
        title: 'Disk pressure',
        kind: 'runbook',
        critical: true,
        diff,
        added: 9,
        removed: 0,
        state: 'waiting',
        sources: [{ key: 'fixes:F-3', title: 'Fix F-3: Disk pressure on workers', date: '2026-11-01T09:00:00Z', url: '#/cases/mycroft/fixes/F-3' }],
        contradicts: [{ fix: 'F-7', title: 'Grow the disk', cases: 4, why: 'the source deletes logs where F-7 grows the disk' }],
      },
    ],
  }

  it('flags it on the closed card, and names the fix, its cases and why on the page, with the agreed fix linked as a source', () => {
    render(<DraftRunCard run={run} onChanged={() => {}} />)
    expect(screen.getByTestId('knowledge-run-contradicts').textContent).toBe('Contradicts a fix')
    fireEvent.click(screen.getByTestId('knowledge-run-open'))
    const flag = screen.getByTestId('knowledge-page-contradicts')
    expect(flag.textContent).toBe('Contradicts F-7 “Grow the disk”, used in 4 cases: the source deletes logs where F-7 grows the disk. Decide which is right before approving.')
    expect(flag.querySelector('a')!.getAttribute('href')).toBe('#/cases/mycroft/fixes/F-7')
    const source = screen.getByText('Fix F-3: Disk pressure on workers')
    expect(source.getAttribute('href')).toBe('#/cases/mycroft/fixes/F-3')
  })

  it('shows no flag when no page contradicts a fix', () => {
    render(<DraftRunCard run={{ ...run, pages: [{ ...run.pages[0]!, contradicts: undefined }] }} onChanged={() => {}} open />)
    expect(screen.queryByTestId('knowledge-run-contradicts')).toBeNull()
    expect(screen.queryByTestId('knowledge-page-contradicts')).toBeNull()
  })
})
