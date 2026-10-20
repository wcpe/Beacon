// 审计动作清单生成器（issue #152）
//
// 真源：apps/server/internal/model/enums.go 中的 model.Action* = "..." 常量。
// 产出：apps/web/src/pages/audits/audit-actions.generated.ts
//
// 为什么生成而不是手写：后端审计动作已增至 157 项（config / file / delivery / identity …），
// 审计页手写清单只覆盖 57 项且必然滞后——运维在动作筛选里选不到后端已写入的动作，只能手工拼 ?action=。
// 生成后追平只需 `pnpm gen:audit-actions` 一条命令，且分组顺序由同一规则保证稳定，不会因手工编辑而走形。
//
// 用法：
//   node scripts/gen-audit-actions.mjs          # 重写生成物
//   node scripts/gen-audit-actions.mjs --check  # 只校验：生成物落后于 enums.go 时退出码 1（供 CI / 本地自查）

import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '..')
const enumsPath = resolve(repoRoot, 'apps/server/internal/model/enums.go')
const outPath = resolve(repoRoot, 'apps/web/src/pages/audits/audit-actions.generated.ts')

// 与 apps/web/src/test/observability 的守护测试同一正则：行首（允许缩进）的 Action* = "字面值"
const ACTION_PATTERN = /^\s*Action[A-Za-z0-9_]*\s*=\s*"([^"]+)"/gm

/** 从 enums.go 提取全部审计动作字面值（去重，保持声明顺序） */
function readBackendActions() {
  const source = readFileSync(enumsPath, 'utf8')
  const seen = new Set()
  const actions = []
  for (const match of source.matchAll(ACTION_PATTERN)) {
    const action = match[1]
    if (seen.has(action)) continue
    seen.add(action)
    actions.push(action)
  }
  if (actions.length === 0) {
    throw new Error(`未从 ${relative(repoRoot, enumsPath)} 提取到任何 Action 常量——正则或真源路径已失效`)
  }
  return actions
}

/** 动作前缀 = 首个 '.' 之前的部分（instance / file / delivery …）；无点号时整体视为前缀 */
function prefixOf(action) {
  const dot = action.indexOf('.')
  return dot === -1 ? action : action.slice(0, dot)
}

/**
 * 按动作前缀分组排序：组序取前缀在 enums.go 的首次出现顺序，组内保持声明顺序。
 * enums.go 的声明顺序并非严格按域连续（如 file.* 分布在三处），重排后同域动作才真正相邻。
 */
function groupByPrefix(actions) {
  const groupOrder = new Map()
  for (const action of actions) {
    const prefix = prefixOf(action)
    if (!groupOrder.has(prefix)) groupOrder.set(prefix, groupOrder.size)
  }
  return actions
    .map((action, index) => ({ action, index, group: groupOrder.get(prefixOf(action)) ?? 0 }))
    .sort((left, right) => left.group - right.group || left.index - right.index)
    .map((entry) => entry.action)
}

/** 渲染生成物全文（与磁盘内容逐字节比对即为同步判据） */
function render(actions) {
  const body = actions.map((action) => `  '${action}',`).join('\n')
  return `// 本文件由 scripts/gen-audit-actions.mjs 自动生成，请勿手工编辑。
// 重新生成：pnpm gen:audit-actions；校验是否落后：pnpm check:audit-actions
//
// 真源：apps/server/internal/model/enums.go 的 model.Action* 常量，当前共 ${actions.length} 个。
// 顺序规则：按动作前缀（首个 '.' 之前）分组，组序取前缀在 enums.go 的首次出现顺序，
// 组内保持声明顺序——因此审计页动作筛选里同域动作天然相邻，便于按域扫读。

export const AUDIT_ACTIONS = [
${body}
] as const
`
}

const actions = groupByPrefix(readBackendActions())
const next = render(actions)

if (process.argv.includes('--check')) {
  let current = ''
  try {
    current = readFileSync(outPath, 'utf8')
  } catch {
    current = ''
  }
  if (current !== next) {
    console.error(
      `审计动作生成物已落后于 enums.go（真源 ${String(actions.length)} 项）——请运行 pnpm gen:audit-actions`,
    )
    process.exit(1)
  }
  console.log(`审计动作生成物与 enums.go 同步（${String(actions.length)} 项）。`)
} else {
  writeFileSync(outPath, next, 'utf8')
  console.log(`已生成 ${relative(repoRoot, outPath)}（${String(actions.length)} 项）。`)
}
