// 配置编辑器客户端格式校验（FR-75，增强 FR-1/FR-3）。
//
// 在浏览器即时解析编辑器内容，解析失败时返回首个错误（行号 + 信息），
// 供编辑器行内标错并禁用发布入口。纯函数、无副作用、可穷举单测。
//
// 与控制面 FR-27 服务端 schema 校验互补：FR-27 是发布路径的权威闸，
// 本模块是前置的即时反馈（前端可被绕过，不替代服务端校验）。
//
// YAML 校验说明：当前前端未引入 js-yaml 等 YAML 解析库（见 .claude 规则 15，
// 加依赖须先确认），故 YAML 采用「轻量结构校验」——只逮明显坏格式
// （禁 Tab 缩进 / 引号未闭合 / 流式括号未闭合 / 基本 key: 结构缺失），
// 不做完整 YAML 1.2 语义解析。漏判由 FR-27 服务端兜底。

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

// ---- YAML：轻量结构校验 ----

function lintYaml(content: string): LintError | null {
  const lines = content.split('\n')

  // 跨行累计的引号 / 括号状态：用于检测整篇未闭合
  let openQuote: '' | "'" | '"' = ''
  let quoteStartLine = 0
  let bracketDepth = 0
  let bracketStartLine = 0

  for (let i = 0; i < lines.length; i++) {
    const lineNo = i + 1
    const raw = lines[i]

    // 续行（处于未闭合引号 / 括号中）只更新配对状态，不做结构校验
    const inContinuation = openQuote !== '' || bracketDepth > 0

    if (!inContinuation) {
      // 1) 缩进禁用 Tab（YAML 规范：缩进只能用空格）
      const indent = raw.match(/^[ \t]*/)![0]
      if (indent.includes('\t')) {
        return { line: lineNo, message: 'YAML 缩进不能使用 Tab，请改用空格' }
      }

      const trimmed = raw.trim()
      // 空行 / 注释 / 文档标记跳过
      if (trimmed === '' || trimmed.startsWith('#') || trimmed === '---' || trimmed === '...') {
        continue
      }

      // 2) 映射行须含 `key:` 结构（数组项 `- ` 与块标量续行除外）
      if (!isStructurallyValidYamlLine(trimmed)) {
        return { line: lineNo, message: '缺少有效的「键: 值」结构或数组项' }
      }
    }

    // 3) 逐字符更新引号 / 括号配对（跨行累计）
    const scan = scanQuotesAndBrackets(raw, openQuote, bracketDepth)
    if (openQuote === '' && scan.openQuote !== '') quoteStartLine = lineNo
    if (bracketDepth === 0 && scan.bracketDepth > 0) bracketStartLine = lineNo
    openQuote = scan.openQuote
    bracketDepth = scan.bracketDepth
    if (scan.bracketDepth < 0) {
      return { line: lineNo, message: '出现多余的右括号' }
    }
  }

  // 文档结束仍有未闭合引号 / 括号
  if (openQuote !== '') {
    return { line: quoteStartLine, message: `引号 ${openQuote} 未闭合` }
  }
  if (bracketDepth > 0) {
    return { line: bracketStartLine, message: '括号未闭合' }
  }
  return null
}

// 判断一个去首尾空白的 YAML 行结构是否合法（保守：只逮明显非法）。
function isStructurallyValidYamlLine(trimmed: string): boolean {
  // 数组项：`- ...` 或单独 `-`
  if (trimmed === '-' || trimmed.startsWith('- ')) return true
  // 流式集合整行（如 `[1, 2]` / `{a: 1}`）允许
  if (/^[[{]/.test(trimmed)) return true
  // 映射键：行内须出现冒号分隔（`key:` 或 `key: value`）
  if (/^[^:]+:(\s.*|)$/.test(trimmed)) return true
  // 「- key: value」形式（数组项里的映射）
  if (/^-\s+[^:]+:/.test(trimmed)) return true
  return false
}

// 逐字符扫描一行，更新跨行引号 / 括号状态。
// 注释（不在引号内的 #）截断后续扫描。
function scanQuotesAndBrackets(
  line: string,
  openQuote: '' | "'" | '"',
  bracketDepth: number,
): { openQuote: '' | "'" | '"'; bracketDepth: number } {
  let q = openQuote
  let depth = bracketDepth
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (q !== '') {
      // 引号内：遇到同种引号即闭合
      if (ch === q) q = ''
      continue
    }
    // 不在引号内
    if (ch === '#') break // 行内注释，忽略其后
    if (ch === "'" || ch === '"') {
      q = ch
    } else if (ch === '[' || ch === '{' || ch === '(') {
      depth++
    } else if (ch === ']' || ch === '}' || ch === ')') {
      depth--
      if (depth < 0) return { openQuote: q, bracketDepth: depth }
    }
  }
  return { openQuote: q, bracketDepth: depth }
}
