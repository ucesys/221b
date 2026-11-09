// Hosts outside Kubernetes (Phase 12, docs/automation.md): added from the
// Nodes tab, their key's fingerprint confirmed before any credential is
// sent, paired with a password through sudo, then nodes for Automation,
// and removed. The host is the simulator's fake SSH host, started here.
import { spawn, type ChildProcess } from 'node:child_process'
import { expect, test } from './test'
import { inKind } from './cluster'

const SAME = { 'Sec-Fetch-Site': 'same-origin' }

test.skip(inKind, 'the fake host runs beside the simulator, which the kind cluster cannot reach')

let host: ChildProcess | undefined
let port = 0

test.beforeAll(async () => {
  host = spawn('../bin/221b-sim', ['-ssh-host', '127.0.0.1:0', '-ssh-host-only'])
  port = await new Promise<number>((resolve, reject) => {
    let said = ''
    const timer = setTimeout(() => reject(new Error(`the fake host did not start: ${said}`)), 10_000)
    host!.stderr!.on('data', (b: Buffer) => {
      said += b.toString()
      const m = /fake SSH host on 127\.0\.0\.1:(\d+)/.exec(said)
      if (m) {
        clearTimeout(timer)
        resolve(Number(m[1]))
      }
    })
  })
})

test.afterAll(() => {
  host?.kill()
})

test('a host is added, its key confirmed, paired with a password, a node for Automation, and removed', async ({ page, request }) => {
  const name = `e2e-host-${Date.now()}-${test.info().workerIndex}`
  await page.goto('/#/nodes')
  await expect(page.getByTestId('cluster-state')).toHaveText('Connected')
  await page.getByTestId('host-add').click()
  const dialog = page.getByTestId('host-dialog')
  await dialog.getByTestId('host-list').fill(`${name}=127.0.0.1`)
  await dialog.getByTestId('host-port').fill(String(port))
  await dialog.getByTestId('host-labels').fill('role=e2e-backup')
  // The account to pair with comes with the host; it is sent once the key is trusted.
  await dialog.getByTestId('host-cred-user').fill('admin')
  await dialog.getByTestId('host-cred-password').fill('not-the-password')
  await dialog.getByTestId('host-next').click()

  // The fingerprint, never the key, and the command to compare it with.
  await expect(dialog.getByTestId('host-fingerprint')).toHaveText(/^SHA256:[A-Za-z0-9+/]{43}$/)
  await expect(dialog.getByTestId('host-keygen')).toContainText('ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub')
  await expect(dialog).not.toContainText('AAAA')
  await expect(dialog.getByTestId('host-trust')).toHaveText('Trust this key and pair')
  await dialog.getByTestId('host-trust').click()
  await expect(dialog).toHaveAttribute('data-step', 'pair')

  // A wrong password is refused, and the field is emptied.
  await expect(dialog.getByTestId('host-error')).toContainText("refused admin's password")
  await expect(dialog.getByTestId('host-cred-password')).toHaveValue('')
  await dialog.getByTestId('host-cred-password').fill('host-password')
  await dialog.getByTestId('host-pair').click()
  await expect(dialog.getByTestId('host-done')).toHaveText(`${name} is paired. Automation reaches it as a node, as root.`)
  await dialog.getByTestId('host-close').click()
  await expect(dialog).toHaveCount(0)

  // On the Nodes tab, tagged a host, with Hosts among the roles.
  await page.goto('/#/nodes?role=host')
  const row = page.locator(`[data-node="${name}"]`)
  await expect(row).toBeVisible()
  await expect(row.locator('.rk-role')).toHaveText('host')
  await expect(page.getByRole('group', { name: 'Show nodes by role' }).getByRole('button', { name: /Hosts/ })).toHaveAttribute('aria-pressed', 'true')

  // Automation sees it paired, and its facts are read over SSH.
  const fleets = await (await request.get('/api/v1/automation/fleets')).json()
  const all = fleets.fleets.at(-1)
  expect(all.nodes).toContainEqual(expect.objectContaining({ name, host: true, paired: true, loginUser: 'root' }))
  const facts = await request.post('/api/v1/automation/facts', { data: { nodes: [name] }, headers: SAME })
  expect(facts.status()).toBe(200)
  expect((await facts.json()).nodes[0].facts['os.family']).toBe('debian')

  // Its details, then Remove, with the cleanup command shown first.
  await row.click()
  const panel = page.getByTestId('node-panel')
  await expect(panel).toHaveAttribute('data-host', 'true')
  await expect(panel.getByTestId('node-status')).toContainText('Paired')
  await expect(panel.getByTestId('host-connection')).toContainText(`127.0.0.1:${port}`)
  await expect(panel.getByTestId('host-connection')).toContainText('SHA256:')
  await expect(panel).not.toContainText('AAAA')
  await panel.getByTestId('host-remove').click()
  await expect(page.getByTestId('host-cleanup')).toContainText("sudo sh <<'221B-CLEANUP'")
  await page.getByTestId('confirm-ok').click()
  await expect(row).toHaveCount(0)
})
