// P1-13: every value in the table matches `kubectl describe node`. Runs on
// the kind cluster only, where kubectl can describe the same nodes.
import { expect, test } from './test'
import { fCores } from '../src/lib/format'
import { inKind, kubectl, nodeRows, snapshot } from './cluster'

test.skip(!inKind, 'needs kubectl and a real cluster')

/** A CPU quantity in millicores: "950m" → 950, "2" → 2000. */
function milli(q: string): number {
  return q.endsWith('m') ? Number(q.slice(0, -1)) : Math.round(Number(q) * 1000)
}

const UNITS: Record<string, number> = { Ki: 2 ** 10, Mi: 2 ** 20, Gi: 2 ** 30, Ti: 2 ** 40, k: 1e3, M: 1e6, G: 1e9, T: 1e12 }

/** A memory quantity in bytes: "290Mi", "1Gi", "512M", "123456". */
function bytes(q: string): number {
  const m = /^(\d+(?:\.\d+)?)([A-Za-z]*)$/.exec(q)
  if (!m) throw new Error(`not a quantity: ${q}`)
  return Math.round(Number(m[1]) * (m[2] ? (UNITS[m[2]] ?? NaN) : 1))
}

function field(text: string, label: string): string {
  const m = new RegExp(`^\\s*${label}:[ \\t]*(.*)$`, 'm').exec(text)
  return (m?.[1] ?? '').trim()
}

test('the table matches kubectl describe node', async ({ page, request }) => {
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  const rows = nodeRows(await snapshot(request))
  const names = (await kubectl('get', 'nodes', '-o', 'jsonpath={.items[*].metadata.name}')).trim().split(/\s+/)
  expect(rows.map((r) => r.name).sort()).toEqual([...names].sort())

  for (const name of names) {
    const d = await kubectl('describe', 'node', name)
    const row = rows.find((r) => r.name === name)!
    const where = `node ${name}`

    // Allocated resources: requests and limits, as the scheduler counts them.
    const alloc = d.slice(d.indexOf('Allocated resources:'))
    const cpu = /^\s*cpu\s+(\S+)\s+\(\d+%\)\s+(\S+)\s+\(\d+%\)/m.exec(alloc)!
    const mem = /^\s*memory\s+(\S+)\s+\(\d+%\)\s+(\S+)\s+\(\d+%\)/m.exec(alloc)!
    expect(row.cpu.requested, `${where} CPU requested`).toBe(milli(cpu[1]!))
    expect(row.cpu.limits, `${where} CPU limits`).toBe(milli(cpu[2]!))
    expect(row.memory.requested, `${where} memory requested`).toBe(bytes(mem[1]!))
    expect(row.memory.limits, `${where} memory limits`).toBe(bytes(mem[2]!))

    // Allocatable and the pod count.
    const allocatable = d.slice(d.indexOf('Allocatable:'), d.indexOf('System Info:'))
    expect(row.cpu.allocatable, `${where} CPU allocatable`).toBe(milli(field(allocatable, 'cpu')))
    expect(row.memory.allocatable, `${where} memory allocatable`).toBe(bytes(field(allocatable, 'memory')))
    expect(row.pods.capacity, `${where} pod capacity`).toBe(Number(field(allocatable, 'pods')))
    expect(row.pods.running, `${where} pods`).toBe(Number(/Non-terminated Pods:\s+\((\d+) in total\)/.exec(d)![1]))

    // System info, address, scheduling and taints.
    expect(row.kubelet, `${where} kubelet`).toBe(field(d, 'Kubelet Version'))
    expect(row.kernel, `${where} kernel`).toBe(field(d, 'Kernel Version'))
    expect(row.os, `${where} OS`).toBe(field(d, 'OS Image'))
    expect(row.runtime, `${where} runtime`).toBe(field(d, 'Container Runtime Version'))
    expect(row.arch, `${where} architecture`).toBe(field(d, 'Architecture'))
    expect(row.internalIP, `${where} internal IP`).toBe(field(d, 'InternalIP'))
    expect(row.cordoned, `${where} cordoned`).toBe(field(d, 'Unschedulable') === 'true')
    const taints = field(d, 'Taints')
    expect(row.taints.length === 0, `${where} taints`).toBe(taints === '<none>')
    expect(row.role, `${where} role`).toBe(field(d, 'Roles').includes('control-plane') ? 'control-plane' : 'worker')

    // Uptime: the boot time each kubelet reports in its Summary API.
    const summary = JSON.parse(await kubectl('get', '--raw', `/api/v1/nodes/${name}/proxy/stats/summary?only_cpu_and_memory=true`)) as { node: { startTime: string } }
    expect(row.bootedAt && Date.parse(row.bootedAt), `${where} boot time`).toBe(Date.parse(summary.node.startTime))

    // And the screen shows those numbers.
    const cells = page.locator(`[data-node="${name}"] > span`)
    await expect(cells.nth(4)).toContainText(fCores(row.cpu.requested))
    await expect(cells.nth(5)).toHaveText(fCores(row.cpu.allocatable))
    await expect(cells.nth(6)).toHaveText(fCores(Math.max(0, row.cpu.allocatable - row.cpu.requested)))
    await expect(cells.nth(10)).toHaveText(`${row.pods.running} / ${row.pods.capacity}`)
  }
})
