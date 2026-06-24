// 配置编辑器客户端格式校验（FR-75，增强 FR-1/FR-3）。
//
// 在浏览器即时解析编辑器内容，解析失败时返回首个错误（行号 + 信息），
// 供编辑器行内标错并禁用发布入口。纯函数、无副作用、可穷举单测。
//
// 与控制面 FR-27 服务端 schema 校验互补：FR-27 是发布路径的权威闸，
// 本模块是前置的即时反馈（前端可被绕过，不替代服务端校验）。
//
// YAML 校验用 js-yaml 做完整 YAML 1.2 解析（锚点 / 别名 / 多文档 / 深层流式集合皆覆盖）；
// JSON 用内置 JSON.parse。解析失败时尽力把错误位置映射到行号。

import { load as loadYaml, YAMLException } from 'js-yaml'

// 单条格式错误：定位行号（从 1 起）+ 中文信息
export interface LintError {
  line: number
  message: string
}

// 按声明格式校验内容；合法或不校验返回 null。
export function lintContent(format: string, content: string): LintError | null {
  // 空内容（仅空白）放行：该层不贡献，合法（与 FR-27 一致）
  if (content.trim() === '') return null

  switch (format) {
    case 'json':
      return lintJson(content)
    case 'yaml':
      return lintYaml(content)
    // properties / plaintext 等无明确语法约束的格式不做格式解析
    default:
      return null
  }
}

// ---- JSON：用内置 JSON.parse，尽力把错误位置映射到行号 ----

function lintJson(content: string): LintError | null {
  try {
    JSON.parse(content)
    return null
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e)
    return { line: jsonErrorLine(content, message), message }
  }
}

// 从 V8 的 JSON 错误信息里提取出错位置并换算行号；提取不到归到第 1 行。
function jsonErrorLine(content: string, message: string): number {
  // 形如 "... at position 12" 或 "... at line 3 column 5"
  const lineMatch = message.match(/line (\d+)/i)
  if (lineMatch) return Number(lineMatch[1])

  const posMatch = message.match(/position (\d+)/i)
  if (posMatch) {
    const pos = Number(posMatch[1])
    // 统计 position 之前的换行数 → 行号
    let line = 1
    for (let i = 0; i < pos && i < content.length; i++) {
      if (content[i] === '\n') line++
    }
    return line
  }
  return 1
}

// ---- YAML：用 js-yaml 完整解析 ----

function lintYaml(content: string): LintError | null {
  try {
    // 配置文件通常单文档，load 已覆盖单文档全部语法错误且更快。
    loadYaml(content)
    return null
  } catch (e) {
    if (e instanceof YAMLException) {
      // js-yaml 的 mark.line 从 0 起，换算为从 1 起的行号；mark 缺失归到第 1 行。
      const line = (e.mark?.line ?? 0) + 1
      return { line, message: e.reason || e.message }
    }
    return { line: 1, message: e instanceof Error ? e.message : String(e) }
  }
}
