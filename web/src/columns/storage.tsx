import type { StoragePair } from '../api/storage'
import { fmtTs, minutesSince } from '../lib/format'
import { accessShort, bindingText, bytes, nodeText, pairLabels } from '../lib/volumes'
import type { StorageView } from '../lib/volumes'
import type { ColumnDef } from '../lib/table'

// The Storage tab's columns (ST-06, ST-11). The tab draws its own rows, a
// tree of groups and pods; these say what each column is called, how wide
// it is, how it sorts in All volumes, and what it exports.

/** What a pair's class binds as, when the person may read classes. */
export type BindingOf = (p: StoragePair) => string | undefined

function claimText(p: StoragePair): string {
  if (p.claim) return `${p.namespace}/${p.claim}`
  if (p.wasClaim) return `was ${p.wasClaim}`
  return ''
}

function volumeText(p: StoragePair): string {
  return p.volume && p.state !== 'pending' ? p.volume : ''
}

function col(c: Omit<ColumnDef<StoragePair>, 'cell' | 'min'> & { min?: number }): ColumnDef<StoragePair> {
  return { min: parseInt(c.width, 10) || 80, cell: () => null, ...c }
}

/** Every column, in By class order; the views reorder them. */
export function storageColumns(binding: BindingOf): ColumnDef<StoragePair>[] {
  return [
    col({ id: 'volume', label: 'Volume', sortName: 'volume name', width: '216px', sortValue: (p) => volumeText(p) || null, export: (p) => [{ head: 'Volume', cell: volumeText(p) || null }] }),
    col({ id: 'claim', label: 'Claim', sortName: 'claim', width: '258px', sortValue: (p) => claimText(p) || null, export: (p) => [{ head: 'Claim', cell: claimText(p) || null }] }),
    col({ id: 'class', label: 'Class', width: '120px', sortValue: (p) => p.class || null, export: (p) => [{ head: 'Class', cell: p.class || null, json: p.classMissing ? { name: p.class, missing: true } : p.class || null }] }),
    col({
      id: 'status',
      label: 'Status',
      sortName: 'status (trouble first)',
      width: '190px',
      sortValue: (p) => (p.attention ? '0' : p.reason ? '1' : '2') + p.phase,
      export: (p) => [
        { head: 'Status', cell: p.phase },
        { head: 'Why', cell: p.reason?.text ?? null },
      ],
    }),
    col({ id: 'capacity', label: 'Capacity', width: '72px', right: true, defaultDir: 'desc', sortValue: (p) => (p.state === 'pending' ? (p.requested ?? null) : (p.capacity ?? null)), export: (p) => [{ head: 'Capacity', cell: p.state === 'pending' ? `${bytes(p.requested)} requested` : p.capacity ? bytes(p.capacity) : null, json: p.state === 'pending' ? { requested: p.requested } : p.capacity }] }),
    col({
      id: 'used',
      label: 'Used',
      sortName: 'used (fullest first)',
      width: '160px',
      defaultDir: 'desc',
      sortValue: (p) => p.usage?.pct ?? null,
      export: (p) => [
        { head: 'Used', cell: p.usage ? bytes(p.usage.used) : null, json: p.usage ? p.usage.used : null },
        { head: 'Used %', cell: p.usage ? Math.round(p.usage.pct) : null },
      ],
    }),
    col({ id: 'access', label: 'Access', width: '56px', sortValue: (p) => (p.accessModes ?? []).map(accessShort).join(',') || null, export: (p) => [{ head: 'Access', cell: (p.accessModes ?? []).map(accessShort).join(', ') || null, json: p.accessModes ?? [] }] }),
    col({ id: 'reclaim', label: 'Reclaim', width: '64px', sortValue: (p) => (p.state === 'pending' ? null : p.reclaim || null), export: (p) => [{ head: 'Reclaim', cell: p.state === 'pending' ? null : p.reclaim || null }] }),
    col({
      id: 'mounted',
      label: 'Mounted by',
      width: '200px',
      sortValue: (p) => ((p.pods ?? []).length === 1 ? p.pods![0]!.name : (p.pods ?? []).length ? `${(p.pods ?? []).length} pods` : null),
      export: (p) => [{ head: 'Mounted by', cell: (p.pods ?? []).map((x) => x.name).join('\n') || null, json: (p.pods ?? []).map((x) => ({ pod: x.name, node: x.node ?? null, mountPath: x.mountPath ?? null, readOnly: !!x.readOnly, status: x.status })) }],
    }),
    col({ id: 'node', label: 'Node', width: '110px', sortValue: (p) => nodeText(p) || null, export: (p) => [{ head: 'Node', cell: (p.nodes?.length ? p.nodes : p.source?.node ? [p.source.node] : []).join('\n') || null }] }),
    col({ id: 'age', label: 'Age', width: '56px', right: true, defaultDir: 'asc', sortValue: (p, now) => minutesSince(p.created, now), export: (p) => [{ head: 'Created', cell: fmtTs(p.created) }] }),
    col({ id: 'mode', label: 'Mode', width: '96px', hiddenByDefault: true, sortValue: (p) => p.volumeMode || null, export: (p) => [{ head: 'Mode', cell: p.volumeMode || null }] }),
    col({ id: 'source', label: 'Source', width: '220px', hiddenByDefault: true, sortValue: (p) => p.source?.text ?? null, export: (p) => [{ head: 'Source', cell: p.source?.text ?? null }] }),
    col({ id: 'labels', label: 'Labels', width: '220px', hiddenByDefault: true, sortValue: (p) => pairLabels(p).join(' ') || null, export: (p) => [{ head: 'Labels', cell: pairLabels(p).join('\n') || null, json: pairLabels(p) }] }),
    col({ id: 'release', label: 'Release', width: '120px', hiddenByDefault: true, sortValue: (p) => p.release || null, export: (p) => [{ head: 'Release', cell: p.release || null }] }),
    col({ id: 'binding', label: 'Binding mode', width: '150px', hiddenByDefault: true, sortValue: (p) => binding(p) ?? null, export: (p) => [{ head: 'Binding mode', cell: binding(p) ? bindingText(binding(p)!) : null }] }),
  ]
}

/** The columns of a view, in its order (ST-06, ST-11, ST-12): the first always shows. */
export function viewColumns(all: ColumnDef<StoragePair>[], view: StorageView): ColumnDef<StoragePair>[] {
  const order: Record<StorageView, string[]> = {
    class: ['volume', 'claim', 'status', 'capacity', 'used', 'access', 'reclaim', 'mounted', 'node', 'age', 'mode', 'source', 'labels', 'release', 'binding'],
    namespace: ['claim', 'status', 'capacity', 'used', 'access', 'class', 'volume', 'mounted', 'node', 'age', 'reclaim', 'mode', 'source', 'labels', 'release', 'binding'],
    all: ['volume', 'claim', 'class', 'status', 'capacity', 'used', 'access', 'reclaim', 'mounted', 'node', 'age', 'mode', 'source', 'labels', 'release', 'binding'],
  }
  // By namespace takes the mockup's narrower widths, and All volumes, with
  // a Class column too, narrower still, so each fits 1440 px.
  const widths: Record<StorageView, Record<string, number>> = {
    class: {},
    namespace: { claim: 236, class: 110, volume: 196, mounted: 190 },
    all: { claim: 226, class: 110, volume: 196, mounted: 190, status: 160, used: 150 },
  }
  const byId = new Map(all.map((c) => [c.id, c]))
  return order[view].map((id) => {
    let c = byId.get(id)!
    const w = widths[view][id]
    if (w) c = { ...c, width: `${w}px`, min: w }
    // Reclaim is optional in By namespace, where the mockup leaves it out.
    return view === 'namespace' && id === 'reclaim' ? { ...c, hiddenByDefault: true } : c
  })
}
