import { describe, expect, it } from 'vitest'
import { isWatchedKind, kindNameOf, pluralKind } from './kinds'

describe('kinds', () => {
  it('pluralizes kinds as Kubernetes names their resources', () => {
    // Asked at the Phase 2 review: the Namespaces tab said "Endpointss" and "NetworkPolicys".
    expect(pluralKind('Endpoints')).toBe('Endpoints')
    expect(pluralKind('NetworkPolicy')).toBe('NetworkPolicies')
    expect(pluralKind('Ingress')).toBe('Ingresses')
    expect(pluralKind('Deployment')).toBe('Deployments')
    expect(pluralKind('PersistentVolumeClaim')).toBe('PersistentVolumeClaims')
    // Kinds outside the registry, from a Helm manifest, follow the same rules.
    expect(pluralKind('IngressClass')).toBe('IngressClasses')
    expect(pluralKind('ClusterPolicy')).toBe('ClusterPolicies')
    expect(pluralKind('Gateway')).toBe('Gateways')
    expect(pluralKind('Prometheus')).toBe('Prometheus')
    expect(pluralKind('Certificate')).toBe('Certificates')
  })

  it('names kinds as the server does', () => {
    expect(kindNameOf('Deployment')).toBe('deployments')
    expect(kindNameOf('Endpoints')).toBe('endpoints')
    expect(kindNameOf('NetworkPolicy')).toBe('networkpolicies')
    expect(kindNameOf('Ingress')).toBe('ingresses')
    expect(kindNameOf('IngressClass')).toBe('ingressclasses')
    expect(isWatchedKind('Secret')).toBe(true)
    expect(isWatchedKind('ClusterRole')).toBe(false)
  })
})
