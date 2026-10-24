import test from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import os from 'node:os'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const DIR = __dirname
const SCREENSHOT_DIR = process.env.QUOTA_SCREENSHOT_DIR || path.join(os.tmpdir(), 'quota-demo-screenshots')
fs.mkdirSync(SCREENSHOT_DIR, { recursive: true })

function runAgentBrowserBatch(commands) {
  return new Promise((resolve, reject) => {
    const proc = spawn('npx', ['agent-browser', '--session', 'quota-test-session', 'batch', '--bail'], {
      env: process.env,
    })
    let stdout = ''
    let stderr = ''
    proc.stdout.on('data', (d) => { stdout += d })
    proc.stderr.on('data', (d) => { stderr += d })
    proc.on('close', (code) => {
      if (code === 0) resolve({ stdout, stderr })
      else reject(new Error(`agent-browser batch failed (code ${code}): ${stderr || stdout}`))
    })
    proc.stdin.end(JSON.stringify(commands))
  })
}

test('comprehensive browser validation: 3 sizes, real 375px viewport, zero overflow & full QA', async (t) => {
  // 1. Safe static server bounded to DIR + path.sep
  const server = http.createServer((req, res) => {
    let pathname = 'index.html'
    try {
      const parsedUrl = new URL(req.url, 'http://127.0.0.1')
      const decoded = decodeURIComponent(parsedUrl.pathname).replace(/^\/+/, '')
      if (decoded) pathname = decoded
    } catch {
      res.writeHead(400)
      res.end('Bad Request')
      return
    }

    const safePath = path.normalize(path.join(DIR, pathname))
    const dirWithSep = DIR.endsWith(path.sep) ? DIR : DIR + path.sep
    if (safePath !== DIR && !safePath.startsWith(dirWithSep)) {
      res.writeHead(403)
      res.end('Forbidden')
      return
    }

    if (fs.existsSync(safePath) && fs.statSync(safePath).isFile()) {
      if (safePath.endsWith('.html')) res.setHeader('Content-Type', 'text/html; charset=utf-8')
      else if (safePath.endsWith('.mjs') || safePath.endsWith('.js')) res.setHeader('Content-Type', 'text/javascript; charset=utf-8')
      res.writeHead(200)
      res.end(fs.readFileSync(safePath))
    } else {
      res.writeHead(404)
      res.end('Not found')
    }
  })

  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const port = server.address().port
  const baseUrl = `http://127.0.0.1:${port}/index.html`

  t.after(async () => {
    try {
      const proc = spawn('npx', ['agent-browser', '--session', 'quota-test-session', 'close'], { env: process.env })
      await new Promise((res) => proc.on('close', res))
    } catch {}
    server.close()
  })

  const mockSummary = {
    generated_at: '2026-10-24T16:00:00Z',
    eligible: 6,
    pending: 0,
    groups: [
      {
        provider: 'codex',
        pool: 'codex',
        label: 'Codex / GPT',
        plans: [{ plan: 'Team', count: 3 }],
        stats: {
          total_requests: 10420,
          success_count: 10170,
          failure_count: 249,
          input_tokens: 1000000,
          cache_read_tokens: 800000,
          total_tokens: 1340000000,
        },
        quota: [
          {
            key: 'summary.codex.18000',
            remainingFraction: 0.71,
            window_usage_cost: 3.46,
            window_usage_estimate_cost: 12.0,
            coverage: { next_reset_at: '2026-10-24T16:32:00Z', eligible: 3, current: 3 },
            window: { seconds: 18000 },
          },
          {
            key: 'summary.codex.604800',
            remainingFraction: 0.95,
            window_usage_cost: 1.91,
            window_usage_estimate_cost: 12.0,
            coverage: { next_reset_at: '2026-10-31T05:50:00Z', eligible: 3, current: 3 },
            window: { seconds: 604800 },
          },
        ],
      },
      {
        provider: 'antigravity',
        pool: 'antigravity-gemini-models',
        label: 'Gemini Models',
        plans: [{ plan: 'Pro', count: 3 }],
        stats: {
          total_requests: 20900,
          success_count: 20030,
          failure_count: 870,
          input_tokens: 500000,
          cache_read_tokens: 372500,
          total_tokens: 945740000,
        },
        quota: [
          {
            key: 'summary.antigravity-gemini-models.18000',
            remainingFraction: 0.52,
            window_usage_cost: 5.62,
            window_usage_estimate_cost: 11.8,
            coverage: { next_reset_at: '2026-10-24T20:16:00Z', eligible: 3, current: 2, peer: 1, samples: 4 },
            window: { seconds: 18000 },
          },
          {
            key: 'summary.antigravity-gemini-models.604800',
            remainingFraction: 0.48,
            window_usage_cost: 1.0,
            window_usage_estimate_cost: 30.0,
            coverage: { next_reset_at: '2026-10-30T19:50:00Z', eligible: 3, current: 3 },
            window: { seconds: 604800 },
          },
        ],
      },
    ],
  }

  const partialSummary = {
    generated_at: '2026-10-24T16:05:00Z',
    eligible: 3,
    pending: 0,
    groups: [
      {
        provider: 'codex',
        pool: 'codex',
        label: 'Codex / GPT',
        plans: [{ plan: 'Team', count: 3 }],
        quota: [],
      },
    ],
  }

  // Base64 encode JSON to ensure safe transport inside batch eval
  const mockB64 = Buffer.from(JSON.stringify(mockSummary)).toString('base64')
  const partialB64 = Buffer.from(JSON.stringify(partialSummary)).toString('base64')

  const batchCommands = [
    ['open', baseUrl],
    ['screenshot', path.join(SCREENSHOT_DIR, '01-live-empty-state.png')],
    ['eval', "window.qaResults = { emptyPct: document.getElementById('codex-center-pct').textContent.trim() }"],
    ['eval', "window.updateAccountQuotaStatus({ state: 'loading' })"],
    ['eval', "window.qaResults.loadingPing = document.getElementById('status-dot').className.includes('animate-ping')"],
    ['eval', `window.updateAccountQuota(JSON.parse(atob('${mockB64}')))`],
    ['eval', "window.updateAccountQuotaStatus({ state: 'ok' })"],
    ['eval', "Object.assign(window.qaResults, { codexPct: document.getElementById('codex-center-pct').textContent.trim(), agPct: document.getElementById('ag-center-pct').textContent.trim(), codexUsd: document.getElementById('codex-remain-val').textContent.trim(), agUsd: document.getElementById('ag-remain-val').textContent.trim() })"],
    ['screenshot', path.join(SCREENSHOT_DIR, '02-live-populated-standard.png')],
    ['focus', '#codex-ring-wrapper'],
    ['press', 'Enter'],
    ['eval', "window.qaResults.codexWkPct = document.getElementById('codex-center-pct').textContent.trim()"],
    ['screenshot', path.join(SCREENSHOT_DIR, '03-keyboard-focus-toggled.png')],
    ['click', '#ag-coverage-info'],
    ['eval', "window.qaResults.coverageDetail = document.getElementById('coverage-detail-text').textContent.trim()"],
    ['screenshot', path.join(SCREENSHOT_DIR, '04-independent-coverage-box.png')],
    ['click', '#btn-close-coverage'],
    ['eval', "window.updateAccountQuotaStatus({ state: 'unauthorized', message: '额度接口鉴权失败，请检查宿主中的只读密钥', lastSuccessAt: new Date().toISOString() })"],
    ['eval', "Object.assign(window.qaResults, { bannerText: document.getElementById('status-banner-text').textContent.trim(), bannerInputCount: document.getElementById('status-banner').querySelectorAll('input').length })"],
    ['screenshot', path.join(SCREENSHOT_DIR, '05-unauthorized-401-state.png')],
    ['eval', "window.updateAccountQuotaStatus({ state: 'ok' })"],
    ['set', 'viewport', '375', '812'],
    ['eval', "document.getElementById('AccountQuotaCard').className = 'apple-widget-card mode-standard'"],
    ['screenshot', path.join(SCREENSHOT_DIR, '06-mobile-375-standard.png')],
    ['eval', "document.getElementById('AccountQuotaCard').className = 'apple-widget-card mode-compact'"],
    ['screenshot', path.join(SCREENSHOT_DIR, '07-mobile-375-compact.png')],
    ['eval', "document.getElementById('AccountQuotaCard').className = 'apple-widget-card mode-mini'"],
    ['screenshot', path.join(SCREENSHOT_DIR, '08-mobile-375-mini.png')],
    ['eval', "(() => { const card = document.getElementById('AccountQuotaCard'); const codexBox = document.getElementById('codex-quota-box'); const agBox = document.getElementById('ag-quota-box'); window.qaResults.boundsCheck = { docNoOverflow: document.documentElement.scrollWidth <= window.innerWidth, cardBounded: card.scrollWidth <= card.clientWidth, codexBoxBounded: codexBox.scrollWidth <= codexBox.clientWidth, agBoxBounded: agBox.scrollWidth <= agBox.clientWidth }; })()"],
    // 切回 standard 模式测试详情在覆盖更新与缺组下的自动重绘与自动关闭（避免在 mini 隐藏 header 下 click 超时）
    ['eval', "document.getElementById('AccountQuotaCard').className = 'apple-widget-card mode-standard'"],
    ['click', '#ag-coverage-info'],
    ['eval', `window.updateAccountQuota(JSON.parse(atob('${partialB64}')))`],
    ['eval', "Object.assign(window.qaResults, { agClearedPct: document.getElementById('ag-center-pct').textContent.trim(), agClearedTitle: document.getElementById('ag-title').textContent.trim(), coverageClosedOnClear: document.getElementById('coverage-detail-box').classList.contains('hidden'), pageErrors: window.__pageErrors })"],
    ['eval', 'JSON.stringify(window.qaResults)'],
  ]

  const { stdout } = await runAgentBrowserBatch(batchCommands)
  const lines = stdout.trim().split('\n')
  const jsonStr = lines[lines.length - 1]
  const qa = JSON.parse(JSON.parse(jsonStr))

  assert.equal(qa.emptyPct, '—', 'Initial empty state center pct should be dash')
  assert.equal(qa.loadingPing, true, 'Loading status should animate ping')
  assert.equal(qa.codexPct, '71%', 'Codex 5h pct should be 71%')
  assert.equal(qa.agPct, '52%', 'Antigravity 5h pct should be 52%')
  assert.equal(qa.codexUsd, '$8.54')
  assert.equal(qa.agUsd, '$6.18')
  assert.equal(qa.codexWkPct, '95%', 'Keyboard Enter should toggle to weekly 95%')
  assert.ok(qa.coverageDetail.includes('覆盖凭证: 2/3'))
  assert.match(qa.bannerText, /检查宿主中的只读密钥/)
  assert.equal(qa.bannerInputCount, 0, 'No input elements in 401 banner')
  assert.equal(qa.boundsCheck.docNoOverflow, true, 'No horizontal overflow on document at 375px')
  assert.equal(qa.boundsCheck.cardBounded, true, 'Card fits within client width at 375px')
  assert.equal(qa.boundsCheck.codexBoxBounded, true, 'Codex quota box fits within client width')
  assert.equal(qa.boundsCheck.agBoxBounded, true, 'AG quota box fits within client width')
  assert.equal(qa.agClearedPct, '—', 'Missing AG group should clear to dash')
  assert.equal(qa.agClearedTitle, '反重力 · Gemini', 'Missing AG group restores default title')
  assert.equal(qa.coverageClosedOnClear, true, 'Coverage detail auto-closed on group removal')
  assert.deepEqual(qa.pageErrors, [], 'Zero page errors throughout full QA')
})
