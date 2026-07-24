// 千级服务器页面基准：登录 mock 演示模式后逐页记录加载耗时与 DOM 规模。
// 不依赖 package.json 脚本，直接执行：node web/scripts/benchmark-server-scale.mjs

import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { performance } from 'node:perf_hooks'

const repoRoot = fileURLToPath(new URL('../..', import.meta.url))
const baseUrl = process.env.BENCH_BASE_URL ?? 'http://127.0.0.1:5180'
const outputArg = process.argv[2]
const outputPath = outputArg
  ? resolve(process.cwd(), outputArg)
  : join(repoRoot, '.tmp', `server-scale-benchmark-${stamp()}.json`)

const pages = [
  { name: 'configs', path: '/configs' },
  { name: 'dashboard', path: '/dashboard' },
  { name: 'servers', path: '/servers' },
  { name: 'zones', path: '/zones' },
  { name: 'topology', path: '/topology' },
  { name: 'file-preview', path: '/file-preview' },
  { name: 'file-sync', path: '/file-sync' },
  { name: 'wallboard', path: '/wallboard' },
]

function stamp() {
  return new Date().toISOString().replace(/[:.]/g, '-')
}

function url(path) {
  return new URL(path, baseUrl).toString()
}

async function login(page) {
  await page.goto(url('/login'), { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: /演示模式/ }).click()
  await page.waitForURL(/\/configs/, { timeout: 10_000 })
}

async function measurePage(page, target) {
  const errors = []
  const onConsole = (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
  }
  const onPageError = (err) => errors.push(err.message)
  page.on('console', onConsole)
  page.on('pageerror', onPageError)

  const started = performance.now()
  await page.goto(url(target.path), { waitUntil: 'domcontentloaded' })
  await page.waitForLoadState('networkidle', { timeout: 10_000 }).catch(() => undefined)
  await page.waitForTimeout(250)
  const loadMs = Math.round(performance.now() - started)

  const metrics = await page.evaluate(() => {
    const doc = globalThis.document
    const all = doc.querySelectorAll('*')
    const nav = performance.getEntriesByType('navigation')[0]
    return {
      domNodes: all.length,
      inputs: doc.querySelectorAll('input').length,
      checkboxes: doc.querySelectorAll('input[type="checkbox"]').length,
      options: doc.querySelectorAll('option').length,
      tableRows: doc.querySelectorAll('tbody tr').length,
      buttons: doc.querySelectorAll('button').length,
      links: doc.querySelectorAll('a').length,
      scrollHeight: doc.documentElement.scrollHeight,
      transferSize: nav && 'transferSize' in nav ? nav.transferSize : 0,
    }
  })

  page.off('console', onConsole)
  page.off('pageerror', onPageError)
  return { ...target, loadMs, ...metrics, errors }
}

async function main() {
  const browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const page = await context.newPage()
  await login(page)

  const results = []
  for (const target of pages) {
    results.push(await measurePage(page, target))
  }

  await browser.close()
  const report = { baseUrl, createdAt: new Date().toISOString(), results }
  await mkdir(dirname(outputPath), { recursive: true })
  await writeFile(outputPath, JSON.stringify(report, null, 2), 'utf8')

  console.table(
    results.map((r) => ({
      page: r.name,
      loadMs: r.loadMs,
      domNodes: r.domNodes,
      inputs: r.inputs,
      checkboxes: r.checkboxes,
      options: r.options,
      tableRows: r.tableRows,
      scrollHeight: r.scrollHeight,
      errors: r.errors.length,
    })),
  )
  console.log(`基准结果已写入：${outputPath}`)
}

main().catch((err) => {
  console.error(err)
  process.exit(1)
})
