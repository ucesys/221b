import { describe, expect, it } from 'vitest'
import { parseBlocks, parseInline } from './markdown'

describe('markdown', () => {
  it('reads paragraphs, lists, code and tables', () => {
    const b = parseBlocks('The cause.\nMore of it.\n\n- one\n- two\n  continued\n\n1. first\n2. second\n\n```sh\nkubectl get pods\n```\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n')
    expect(b.map((x) => x.t)).toEqual(['p', 'ul', 'ol', 'code', 'table'])
    expect(b[0]).toEqual({ t: 'p', text: 'The cause. More of it.' })
    expect(b[1]).toEqual({ t: 'ul', items: ['one', 'two continued'] })
    expect(b[3]).toEqual({ t: 'code', lang: 'sh', text: 'kubectl get pods', open: false })
    expect(b[4]).toEqual({ t: 'table', head: ['A', 'B'], rows: [['1', '2']] })
  })

  it('runs an unclosed fence to the end, as an answer arrives', () => {
    expect(parseBlocks('Run:\n\n```\nkubectl drain')).toEqual([
      { t: 'p', text: 'Run:' },
      { t: 'code', lang: '', text: 'kubectl drain', open: true },
    ])
  })

  it('reads code, bold and links inline, and nothing else as markup', () => {
    expect(parseInline('Pod [web/api-0](221b://Pod/web/api-0) is **down**: `CrashLoopBackOff` <script>')).toEqual([
      { t: 'text', text: 'Pod ' },
      { t: 'link', text: 'web/api-0', url: '221b://Pod/web/api-0' },
      { t: 'text', text: ' is ' },
      { t: 'b', text: 'down' },
      { t: 'text', text: ': ' },
      { t: 'code', text: 'CrashLoopBackOff' },
      { t: 'text', text: ' <script>' },
    ])
  })
})
