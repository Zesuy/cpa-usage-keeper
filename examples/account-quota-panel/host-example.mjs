// Run in your host/backend, never as a browser script.
// A JSON-lines bridge lets any desktop/panel host forward the data to its view.
import { createQuotaPoller } from './poller.mjs'

function send(type, data) {
  process.stdout.write(JSON.stringify({ type, data }) + '\n')
}

try {
  const poller = createQuotaPoller({
    baseUrl: process.env.KEEPER_BASE_URL,
    apiKey: process.env.QUOTA_SUMMARY_API_KEY,
    onData: summary => send('quota-summary', summary),
    onStatus: status => send('quota-status', status),
  })
  process.once('SIGINT', () => poller.stop())
  process.once('SIGTERM', () => poller.stop())
  void poller.start()
} catch {
  console.error('Set KEEPER_BASE_URL and QUOTA_SUMMARY_API_KEY in the host environment before starting the poller.')
  process.exitCode = 1
}
