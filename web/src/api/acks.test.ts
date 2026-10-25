import { describe, expect, it } from 'vitest'
import { withAsked } from './acks'
import type { AlertRow } from './types'

const row = (key: string, ack?: AlertRow['ack']): AlertRow => ({ key, kind: 'pod', severity: 'failed', name: key, status: 'Error', statusTone: 'err', events: [], ack })

describe('acknowledgements', () => {
  it('shows what this browser asked for until the server’s rows have it', () => {
    const mine = { by: 'you', at: '2026-10-25T10:00:00Z' }
    const theirs = { by: 'ops@corp.example', at: '2026-10-25T10:00:01Z' }
    const pending = new Map([
      ['local\npod:a', mine],
      ['local\npod:b', null],
    ])
    const out = withAsked([row('pod:a'), row('pod:b', theirs), row('pod:c')], 'local', pending)
    expect(out.map((r) => r.ack)).toEqual([mine, undefined, undefined])
    // Once the rows agree, the server's own words stand.
    expect(withAsked([row('pod:a', theirs)], 'local', pending)[0]!.ack).toEqual(theirs)
    // Another cluster's alert of the same name is not touched.
    expect(withAsked([row('pod:a')], 'dev', pending)[0]!.ack).toBeUndefined()
  })
})
