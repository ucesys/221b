// The Storage tab's data (ST-01 to ST-25): the rows of the storage view,
// what the person may read beyond them, and the drawers. The types follow
// server/internal/views/storage.go and storage_details.go.

import { useEffect, useState } from 'react'
import { useRefreshTick } from './live'
import { apiPath } from '../lib/router'

export interface StorageClassInfo {
  name: string
  provisioner: string
  /** Delete or Retain. */
  reclaim: string
  /** Immediate or WaitForFirstConsumer. */
  binding: string
  expansion: boolean
  default: boolean
  parameters?: Record<string, string>
  mountOptions?: string[]
  topologies?: string[]
  labels?: Record<string, string>
  created: string
}

export type PairState = 'bound' | 'pending' | 'lost' | 'released' | 'available' | 'failed'

export interface Diagnosis {
  text: string
  /** err, warn, info or muted. */
  tone: string
}

export interface VolumeSource {
  kind: 'csi' | 'nfs' | 'local' | 'hostPath' | 'other'
  text: string
  driver?: string
  handle?: string
  fsType?: string
  node?: string
}

export interface ClaimOwner {
  kind: string
  name: string
  template?: string
  ordinal: number
  replicas?: number
}

export interface StoragePod {
  name: string
  node?: string
  status: string
  tone: string
  mountPath?: string
  readOnly?: boolean
  subPath?: string
  container?: string
  since?: string
  note?: string
  why?: string
}

export interface VolumeUsage {
  used: number
  capacity: number
  available: number
  inodesUsed?: number
  inodes?: number
  pct: number
  node?: string
}

/** A volume and its claim, one row of the tree (ST-05). */
export interface StoragePair {
  state: PairState
  namespace?: string
  claim?: string
  volume?: string
  class: string
  classMissing?: boolean
  phase: string
  tone: string
  since?: string
  reason?: Diagnosis
  attention?: boolean
  capacity?: number
  requested?: number
  accessModes?: string[]
  volumeMode?: string
  reclaim?: string
  source?: VolumeSource
  wasClaim?: string
  volumeLabels?: Record<string, string>
  attached?: string[]
  claimLabels?: Record<string, string>
  release?: string
  owner?: ClaimOwner
  resizing?: boolean
  pods?: StoragePod[]
  nodes?: string[]
  usage?: VolumeUsage
  created: string
}

export interface QuotaUse {
  hard: number
  used: number
}

export interface StorageQuota {
  namespace: string
  name: string
  hard?: number
  used?: number
  classes?: Record<string, QuotaUse>
}

export interface StorageRow {
  key: string
  kind: 'sc' | 'pvc' | 'pv' | 'quota'
  class?: StorageClassInfo
  pair?: StoragePair
  quota?: StorageQuota
}

/** What the person may read of the cluster-scoped kinds (ST-02, ST-20). */
export interface StorageScope {
  classes: boolean
  volumes: boolean
  attachments: boolean
}

// ---- the drawers ----

export interface Seg {
  t: string
  b?: boolean
  m?: boolean
  l?: { kind: string; namespace?: string; name: string }
}

export interface StorageHop {
  n: number
  key: string
  label: string
  value: Seg[]
  tag?: string
  desc: Seg[]
  meter?: number
  state: string
  tone: string
}

export interface StorageEvent {
  type: string
  reason: string
  message: string
  count: number
  first?: string
  last?: string
  reportedBy?: string
  pod?: string
  tone: string
}

export interface StorageDetails {
  pair: StoragePair
  class?: StorageClassInfo
  intro: Seg[]
  hops: StorageHop[]
  broken: boolean
  events: StorageEvent[]
  lifecycle: Seg[][]
  noWarningDays: number
  alert?: boolean
  snapshots: string
  attachment?: { name: string; node: string; at: string }
  expansion?: boolean
}

/**
 * What the person may read of classes, volumes and attachments, asked once
 * per cluster and again on each refresh; null until the server answers.
 */
export function useStorageScope(cluster: string): StorageScope | null {
  const tick = useRefreshTick()
  const [scope, setScope] = useState<{ cluster: string; scope: StorageScope } | null>(null)
  useEffect(() => {
    let stale = false
    fetch(apiPath('storage', cluster), { headers: { Accept: 'application/json' } })
      .then(async (r) => {
        if (!stale && r.ok) setScope({ cluster, scope: (await r.json()) as StorageScope })
      })
      .catch(() => undefined)
    return () => {
      stale = true
    }
    // Asked again every minute or so is enough: roles change rarely.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, Math.floor(tick / 20)])
  return scope && scope.cluster === cluster ? scope.scope : null
}

/** The URL of a pair's drawer. */
export function detailsPath(cluster: string, p: Pick<StoragePair, 'namespace' | 'claim' | 'volume'>): string {
  if (p.claim && p.namespace) return apiPath(`storage/claims/${encodeURIComponent(p.namespace)}/${encodeURIComponent(p.claim)}`, cluster)
  return apiPath(`storage/volumes/${encodeURIComponent(p.volume ?? '')}`, cluster)
}
