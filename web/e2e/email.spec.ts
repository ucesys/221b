// Settings › Email (EM-01 to EM-03, EM-10): a SuperAdmin points the console
// at the simulator's SMTP server (make e2e-sim starts it on 127.0.0.1:7525,
// security none), and the test email turns email on.
import { expect, test } from './test'
import { inKind } from './cluster'

const SIM = process.env.E2E_SIM_URL ?? 'http://127.0.0.1:7443'
const SMTP_PORT = process.env.E2E_SMTP_PORT ?? '7525'

interface Captured {
  to: string
  subject: string
}

/** What the simulator's SMTP server took. */
async function taken(): Promise<Captured[]> {
  const res = await fetch(`${SIM}/fake-smtp/messages`)
  if (!res.ok) return []
  return ((await res.json()) as { messages: Captured[] }).messages
}

test.skip(inKind, 'the SMTP server is the simulator’s')

test.afterEach(async ({ request }) => {
  // Email off again for the other tests.
  await request.put('/api/v1/settings/email', { data: { host: '', port: 587, security: 'starttls', username: '', from: '' } })
})

test('a SuperAdmin sets Settings › Email, and a test email turns it on (EM-01 to EM-03)', async ({ page }) => {
  await fetch(`${SIM}/fake-smtp/messages`, { method: 'DELETE' })
  await page.goto('/#/admin/settings/email')
  const status = page.getByTestId('email-status')
  await expect(status).toContainText('Not configured')
  await page.getByTestId('email-host').fill('127.0.0.1')
  await page.getByTestId('email-security').getByRole('button', { name: 'None' }).click()
  await page.getByTestId('email-port').fill(SMTP_PORT)
  await page.getByTestId('email-from').fill('221b@example.com')
  await page.getByTestId('email-save').click()
  await expect(status).toContainText('Configured, not tested')
  await page.getByTestId('email-test-to').fill('e2e@example.com')
  await page.getByTestId('email-test').click()
  await expect(status).toContainText('On: tested')
  await expect.poll(async () => (await taken()).map((m) => `${m.to} ${m.subject}`)).toContain('e2e@example.com 221B: test email')
  // A change of the port turns it off until the next test.
  await page.getByTestId('email-port').fill('2525')
  await page.getByTestId('email-save').click()
  await expect(status).toContainText('Configured, not tested')
})
