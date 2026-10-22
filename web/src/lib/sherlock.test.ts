import { describe, expect, it } from 'vitest'
import { alertQuestion, clusterTroubleshootQuestion, parseRef, podQuestion, refTarget, troubleshootQuestion } from './sherlock'

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
})
