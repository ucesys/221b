// A wiki draft's diff (KN-08): its lines wrap, so a page's long paragraph
// never widens the page, and its front matter is folded into one line.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Diff, frontMatter } from './KnowledgeDraft'

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
