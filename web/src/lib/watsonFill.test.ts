import { describe, expect, it, vi } from 'vitest'
import { onWatsonFill, openWatson } from './watsonFill'

describe('Open in Watson (IB-13)', () => {
  it('hands the cluster and the command to whoever listens, until it stops', () => {
    const got = vi.fn()
    const off = onWatsonFill(got)
    openWatson('prod', 'kubectl rollout restart deployment web -n shop')
    expect(got).toHaveBeenCalledWith({ cluster: 'prod', command: 'kubectl rollout restart deployment web -n shop' })
    off()
    openWatson('prod', 'kubectl get pods')
    expect(got).toHaveBeenCalledTimes(1)
  })
})
