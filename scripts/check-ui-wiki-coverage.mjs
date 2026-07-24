import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '..')
const uiIndex = readFileSync(resolve(repoRoot, 'packages/ui/src/index.ts'), 'utf8')
const coverage = readFileSync(resolve(repoRoot, 'apps/ui-wiki/src/coverage.ts'), 'utf8')

function parseExports(source) {
  const names = new Set()
  const exportBlocks = source.matchAll(/export\s+\{([\s\S]*?)\}\s+from/g)
  for (const block of exportBlocks) {
    for (const rawPart of block[1].split(',')) {
      let part = rawPart.trim()
      if (!part) continue
      if (part.startsWith('type ')) part = part.slice('type '.length).trim()
      const aliased = /\s+as\s+(.+)$/.exec(part)
      names.add((aliased ? aliased[1] : part).trim())
    }
  }
  return [...names].sort()
}

function parseCoverage(source) {
  const match = /UI_WIKI_COVERED_EXPORTS\s*=\s*\[([\s\S]*?)\]\s+as\s+const/.exec(source)
  if (!match) {
    throw new Error('未找到 UI_WIKI_COVERED_EXPORTS 覆盖清单')
  }
  return [...match[1].matchAll(/'([^']+)'/g)].map((m) => m[1]).sort()
}

const exported = parseExports(uiIndex)
const covered = parseCoverage(coverage)
const coveredSet = new Set(covered)
const exportedSet = new Set(exported)
const missing = exported.filter((name) => !coveredSet.has(name))
const extra = covered.filter((name) => !exportedSet.has(name))

if (missing.length > 0 || extra.length > 0) {
  if (missing.length > 0) {
    console.error(`UI 控件博物馆缺少导出展示：${missing.join(', ')}`)
  }
  if (extra.length > 0) {
    console.error(`UI 控件博物馆覆盖清单包含不存在导出：${extra.join(', ')}`)
  }
  process.exit(1)
}

console.log(`UI 控件博物馆覆盖 ${exported.length} 个 UI 包导出。`)
