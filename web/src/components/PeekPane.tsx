import { useCallback, useEffect } from 'react'
import { useView } from '../api/live'
import type { ClusterInfo, NodeRow, PolicyRow } from '../api/types'
import { parsePeek, peekClickHash, peekHref } from '../lib/peek'
import { modelOf } from '../lib/policies'
import { navigate, withParams, type Route } from '../lib/router'
import { Drawer } from './Drawer'
import { NodePanel } from './NodePanel'
import { ObjectPanel } from './ObjectPanel'
import { PodPanel } from './PodPanel'
import { PolicyPanel } from './PolicyPanel'
import { ServicePanel } from './ServicePanel'

interface Props {
  route: Route
  cluster: ClusterInfo | null
  now: number
  onToast: (msg: string) => void
}

/**
 * The details of one object over whatever tab is open (?peek=): a link to
 * a single node, pod, Service, object or policy opens it here rather than
 * switching tabs. Closing it leaves the tab as it was.
 */
export function PeekPane({ route, cluster, now, onToast }: Props) {
  const peek = parsePeek(route.params.get('peek'))
  const nodes = useView<NodeRow>(peek?.kind === 'node' ? 'nodes' : null)
  const policies = useView<PolicyRow>(peek?.kind === 'policy' ? 'policies' : null)
  const close = useCallback(() => navigate(withParams(route, { peek: undefined }), true), [route])

  // Links with data-peek open over the page as it is when clicked, not as
  // it was when the link was drawn.
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      const a = (e.target as Element | null)?.closest?.('a[data-peek]')
      const target = a?.getAttribute('data-peek')
      if (!target) return
      e.preventDefault()
      navigate(peekClickHash(target))
    }
    document.addEventListener('click', onClick)
    return () => document.removeEventListener('click', onClick)
  }, [])

  if (!peek) return null
  switch (peek.kind) {
    case 'node': {
      const r = nodes.rows.find((n) => n.name === peek.name)
      if (!r) return nodes.loaded ? <Missing what={`Node ${peek.name}`} onClose={close} /> : null
      return <NodePanel r={r} metrics={cluster?.metrics === 'available'} metricsNote={cluster?.metricsMessage ?? ''} uptimeNote={cluster?.uptimeMessage ?? ''} now={now} onClose={close} onToast={onToast} />
    }
    case 'pod':
      return <PodPanel key={`${peek.namespace}/${peek.name}`} podKey={`${peek.namespace}/${peek.name}`} row={null} gone={false} cluster={cluster} now={now} onClose={close} onToast={onToast} />
    case 'service':
      return <ServicePanel key={`${peek.namespace}/${peek.name}`} serviceKey={`${peek.namespace}/${peek.name}`} row={null} gone={false} now={now} onClose={close} onToast={onToast} />
    case 'object':
      return (
        <ObjectPanel
          key={`${peek.type}/${peek.namespace}/${peek.name}`}
          kind={peek.type}
          namespace={peek.namespace}
          name={peek.name}
          row={null}
          gone={false}
          cluster={cluster}
          now={now}
          linkTo={(k, n) => peekHref(k === 'Pod' ? { kind: 'pod', namespace: peek.namespace, name: n } : k === 'Service' ? { kind: 'service', namespace: peek.namespace, name: n } : { kind: 'object', type: k, namespace: peek.namespace, name: n })}
          onClose={close}
          onToast={onToast}
        />
      )
    case 'policy':
      return policies.loaded ? <PolicyPanel key={peek.key} keyOf={peek.key} m={modelOf(policies.rows)} now={now} onClose={close} onToast={onToast} /> : null
  }
}

function Missing({ what, onClose }: { what: string; onClose: () => void }) {
  return (
    <Drawer label="Details" kicker="Not found" title={what} onClose={onClose} width={560}>
      <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>It is not in this cluster any more, or Kubernetes does not let you see it.</p>
    </Drawer>
  )
}
