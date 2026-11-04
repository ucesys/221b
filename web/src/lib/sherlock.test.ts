import { renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { alertQuestion, anyOpen, clusterTroubleshootQuestion, opensHere, parseRef, podQuestion, refTarget, troubleshootQuestion, useAnnounce, type PopMessage } from './sherlock'

describe('sherlock', () => {
  it('reads 221b:// links and says where each object opens', () => {
    expect(parseRef('221b://Pod/web/api-0')).toEqual({ kind: 'Pod', namespace: 'web', name: 'api-0' })
    expect(parseRef('221b://Node/worker-1')).toEqual({ kind: 'Node', name: 'worker-1' })
    expect(parseRef('221b://Pod//x')).toBeNull()
    expect(parseRef('https://example.com')).toBeNull()
    expect(refTarget({ kind: 'Pod', namespace: 'web', name: 'api-0' }, 'local')).toEqual({ peek: 'pod:web/api-0' })
    expect(refTarget({ kind: 'Node', name: 'w1' }, 'local')).toEqual({ peek: 'node:w1' })
    expect(refTarget({ kind: 'Deployment', namespace: 'web', name: 'api' }, 'local')).toEqual({ peek: 'object:Deployment/web/api' })
    expect(refTarget({ kind: 'HelmRelease', namespace: 'web', name: 'shop' }, 'dev')).toEqual({ href: '#/c/dev/helm/web/shop' })
    expect(refTarget({ kind: 'Namespace', name: 'web' }, 'local')).toEqual({ href: '#/namespaces/web' })
    expect(refTarget({ kind: 'ClusterRole', name: 'admin' }, 'local')).toBeNull()
  })

  it('asks about what is wrong with a pod, or an alert', () => {
    expect(podQuestion({ namespace: 'web', name: 'a', status: 'CrashLoopBackOff' }, 'web', 'a')).toBe('Why does pod web/a keep crashing?')
    expect(podQuestion({ namespace: 'web', name: 'a', status: 'Pending' }, 'web', 'a')).toBe('Why is pod web/a pending?')
    expect(podQuestion({ namespace: 'web', name: 'a', status: 'Running', lastTermination: 'OOMKilled' }, 'web', 'a')).toBe('Why was pod web/a OOM-killed?')
    expect(podQuestion(null, 'web', 'a')).toBe('Tell me about pod web/a')
    expect(alertQuestion({ kind: 'node', name: 'w1' })).toBe('What is wrong with node w1?')
    expect(alertQuestion({ kind: 'pod', name: 'a', namespace: 'web', status: 'ImagePullBackOff' })).toBe("Why can't pod web/a pull its image?")
    expect(troubleshootQuestion({ kind: 'pod', name: 'a', namespace: 'web', status: 'CrashLoopBackOff', restarts: 7 })).toBe('Troubleshooting report for pod web/a (CrashLoopBackOff, 7 restarts)')
    expect(troubleshootQuestion({ kind: 'pod', name: 'a', namespace: 'web', status: 'Running', oom: true })).toBe('Troubleshooting report for pod web/a (OOM-killed)')
    expect(troubleshootQuestion({ kind: 'node', name: 'w1', status: 'NotReady' })).toBe('Troubleshooting report for node w1 (NotReady)')
    expect(troubleshootQuestion({ kind: 'pvc', name: 'data', namespace: 'db', status: '' })).toBe('Troubleshooting report for volume claim db/data')
    expect(clusterTroubleshootQuestion('prod')).toBe('Troubleshooting report for cluster prod: what is wrong now, why, and how to fix it, most severe first')
  })

  it('opens a link in the main window it is for, and no other', () => {
    expect(opensHere({ cluster: 'dev', window: 'tab2' }, 'tab2', 'dev')).toBe(true)
    expect(opensHere({ cluster: 'dev', window: 'tab2' }, 'tab2', 'prod')).toBe(true)
    expect(opensHere({ cluster: 'dev', window: 'tab2' }, 'tab1', 'prod')).toBe(false)
    expect(opensHere({ cluster: 'dev', window: 'tab2' }, 'tab1', 'dev')).toBe(false)
    // The window it followed has closed: nothing is pulled off another cluster.
    expect(opensHere({ cluster: 'dev', window: '' }, 'tab1', 'dev')).toBe(true)
    expect(opensHere({ cluster: 'dev', window: '' }, 'tab1', 'prod')).toBe(false)
  })

  it('counts a window on show as gone after 5 seconds of silence, a hidden one only after minutes', () => {
    const at = 1_000_000
    expect(anyOpen({ w: { at, hidden: false } }, at + 4_999)).toBe(true)
    expect(anyOpen({ w: { at, hidden: false } }, at + 5_000)).toBe(false)
    // Throttled to a hello a minute, even with one missed.
    expect(anyOpen({ w: { at, hidden: true } }, at + 120_000)).toBe(true)
    expect(anyOpen({ w: { at, hidden: true } }, at + 150_000)).toBe(false)
    expect(anyOpen({}, at)).toBe(false)
  })
})

describe('useAnnounce', () => {
  it('says no hello after its goodbye: a closing page turns hidden after pagehide', async () => {
    const heard: PopMessage[] = []
    const other = new BroadcastChannel('221b-sherlock')
    other.onmessage = (e) => heard.push(e.data as PopMessage)
    const { unmount } = renderHook(() => useAnnounce('local'))
    window.dispatchEvent(new Event('pagehide'))
    document.dispatchEvent(new Event('visibilitychange'))
    await new Promise((r) => setTimeout(r, 30))
    unmount()
    other.close()
    const types = heard.map((m) => m.type)
    expect(types).toContain('bye')
    expect(types.lastIndexOf('hello')).toBeLessThan(types.lastIndexOf('bye'))
  })
})
