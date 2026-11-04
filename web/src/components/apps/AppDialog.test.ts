import { describe, expect, it } from 'vitest'
import type { RepoInspection } from '../../api/apps'
import { inspectFill } from './AppDialog'

const answer: RepoInspection = {
  charts: [{ path: 'charts/web', name: 'web', version: '1.0.0' }] as RepoInspection['charts'],
  ref: 'main',
  refKind: 'branch',
  commit: 'abc',
  readme: { file: 'README.md', name: 'shop', namespace: 'web', values: 'replicaCount: 2\n', params: ['a=1', 'b=2'] },
}
const empty = { path: '', ref: '', name: '', namespace: '', values: '', params: '' }

describe('the repository look-over (M-07)', () => {
  it('fills every empty field', () => {
    expect(inspectFill(empty, answer)).toEqual({ path: 'charts/web', ref: 'main', refKind: 'branch', name: 'shop', namespace: 'web', values: 'replicaCount: 2\n', params: 'a=1\nb=2' })
  })

  it('leaves what is there now', () => {
    expect(inspectFill({ ...empty, path: 'charts/api', name: 'mine', values: 'x: 1' }, answer)).toEqual({ ref: 'main', refKind: 'branch', namespace: 'web', params: 'a=1\nb=2' })
    expect(inspectFill({ ...empty, ref: 'v2' }, answer).refKind).toBeUndefined()
  })

  it('needs no README', () => {
    expect(inspectFill(empty, { ...answer, readme: undefined })).toEqual({ path: 'charts/web', ref: 'main', refKind: 'branch' })
  })
})
