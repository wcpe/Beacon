// 后端审计动作真源读取工具（issue #152）：把「定位并解析 enums.go」这段逻辑收在一处，
// 供审计动作的同源守护（audit-actions-source.test.ts）与 i18n 覆盖守护（audit-action-i18n.test.ts）共用，
// 避免两份正则各自漂移导致其中一个守护悄悄空转。
//
// 注意：本文件不是测试用例（文件名不匹配 vitest 的 test/spec 匹配），只作为测试共用模块。

import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * 定位后端枚举真源 enums.go。
 * vitest 工作目录为 apps/web，jsdom 下 import.meta.url 非 file 协议——
 * 故以 cwd 为主候选向上回溯，测试文件位置为次候选；任一命中即用，全未命中则报错而非静默跳过。
 */
function locateEnumsGo(): string {
  const roots = [process.cwd()]
  try {
    roots.push(dirname(fileURLToPath(import.meta.url)))
  } catch {
    // jsdom 下 import.meta.url 可能非 file 协议，忽略此候选
  }
  const candidates: string[] = []
  for (const root of roots) {
    let dir = resolve(root)
    // 最多向上 6 层：apps/web → apps → 仓库根（容错不同 cwd / 目录布局）
    for (let depth = 0; depth < 6; depth += 1) {
      candidates.push(join(dir, 'apps/server/internal/model/enums.go'))
      candidates.push(join(dir, 'server/internal/model/enums.go'))
      const parent = dirname(dir)
      if (parent === dir) {
        break
      }
      dir = parent
    }
  }
  const hit = candidates.find((candidate) => existsSync(candidate))
  if (hit === undefined) {
    throw new Error(
      `未找到后端枚举真源 enums.go（已尝试 ${String(candidates.length)} 个候选路径，如 ${candidates[0]}）——` +
        '测试无法校验审计动作覆盖，请检查工作目录或路径候选',
    )
  }
  return hit
}

/** enums.go 的绝对路径（模块加载期即定位，路径失配立刻失败而非静默跳过） */
export const ENUMS_GO_PATH = locateEnumsGo()

/** 后端全部审计动作字面值（真源：model.Action* = "..."，保持声明顺序） */
export function readBackendActions(): string[] {
  const source = readFileSync(ENUMS_GO_PATH, 'utf-8')
  const matches = source.matchAll(/^\s*Action[A-Za-z0-9_]*\s*=\s*"([^"]+)"/gm)
  return [...matches].map((m) => m[1])
}

/** 动作前缀 = 首个 '.' 之前的部分（与 scripts/gen-audit-actions.mjs 的分组规则一致） */
export function actionPrefixOf(action: string): string {
  const dot = action.indexOf('.')
  return dot === -1 ? action : action.slice(0, dot)
}
