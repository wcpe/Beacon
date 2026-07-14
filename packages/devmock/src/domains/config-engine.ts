// 配置中心 V2 mock 合并引擎：嵌套结构的解析 / 规范序列化 / 键级深合并（规格 §4.1）
// + 逐键 provenance（叶子路径）+ null 删键记录 + 敏感路径读写工具。
// 语义与后端合并契约对齐（标量覆盖 / map 深合并 / list 整体替换 / null 删键 / 确定性键序输出），
// 供演示模式与 vitest 验证真实交互。解析能力为 demo 取舍：
// - json：全语义（顶层必须是对象）；
// - yaml：缩进简版——嵌套 map 任意层级 + flow 列表（[a, b]），不支持块序列与多行标量；
// - properties：扁平键值（值均为字符串，无 null 删键，值 "null" 是普通字符串）。

import type { ConfigDeletedKey, ConfigFormat, ConfigProvenanceEntry, ConfigScopeLevel } from '@beacon/contracts'

/** 配置值：标量 / 列表 / 嵌套 map；null 仅作层内容中的删键指令，合并结果不含 null */
export type ConfigValue = string | number | boolean | null | ConfigValue[] | ConfigTree

/** 嵌套配置树（map 节点） */
export interface ConfigTree {
  [key: string]: ConfigValue
}

/** 判定值是否为 map 节点（排除 null 与列表） */
export function isTree(value: ConfigValue | undefined): value is ConfigTree {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

// ---- 解析 ----

/** 按格式解析内容为配置树，语法错误抛含行号原因的 Error */
export function parseConfig(format: ConfigFormat, content: string): ConfigTree {
  if (format === 'json') {
    return parseJsonObject(content)
  }
  if (format === 'properties') {
    return parseProperties(content)
  }
  return parseSimpleYaml(content)
}

function parseJsonObject(content: string): ConfigTree {
  let parsed: unknown
  try {
    parsed = JSON.parse(content)
  } catch (error) {
    throw new Error(`JSON 语法错误：${error instanceof Error ? error.message : '解析失败'}`, { cause: error })
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('JSON 顶层必须是对象')
  }
  return parsed as ConfigTree
}

function parseProperties(content: string): ConfigTree {
  const tree: ConfigTree = {}
  const lines = content.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trim()
    if (trimmed === '' || trimmed.startsWith('#')) {
      continue
    }
    const sepIndex = trimmed.indexOf('=')
    if (sepIndex <= 0) {
      throw new Error(`第 ${String(i + 1)} 行语法错误：缺少 "=" 分隔符`)
    }
    // properties 值一律字符串（"null" 也是普通字符串）
    tree[trimmed.slice(0, sepIndex).trim()] = trimmed.slice(sepIndex + 1).trim()
  }
  return tree
}

/** 预处理后的 yaml 行：缩进空格数 + 原始文本 + 行号 */
interface YamlLine {
  indent: number
  text: string
  lineNo: number
}

function parseSimpleYaml(content: string): ConfigTree {
  const lines: YamlLine[] = []
  const raw = content.split('\n')
  for (let i = 0; i < raw.length; i++) {
    const line = raw[i]
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('#')) {
      continue
    }
    if (line.startsWith('\t')) {
      throw new Error(`第 ${String(i + 1)} 行语法错误：缩进不允许使用 Tab`)
    }
    if (trimmed.startsWith('- ') || trimmed === '-') {
      throw new Error(`第 ${String(i + 1)} 行语法错误：不支持块序列，请使用 [a, b] 形式的行内列表`)
    }
    lines.push({ indent: line.length - line.trimStart().length, text: trimmed, lineNo: i + 1 })
  }
  const [tree, next] = buildYamlMap(lines, 0, lines.length > 0 ? lines[0].indent : 0)
  if (next < lines.length) {
    throw new Error(`第 ${String(lines[next].lineNo)} 行语法错误：缩进层级不一致`)
  }
  return tree
}

/** 自 index 起消费缩进为 indent 的同级行，返回 [子树, 下一未消费下标] */
function buildYamlMap(lines: YamlLine[], index: number, indent: number): [ConfigTree, number] {
  const tree: ConfigTree = {}
  let i = index
  while (i < lines.length && lines[i].indent === indent) {
    const { text, lineNo } = lines[i]
    const sepIndex = text.indexOf(':')
    if (sepIndex <= 0) {
      throw new Error(`第 ${String(lineNo)} 行语法错误：缺少 ":" 分隔符`)
    }
    const key = text.slice(0, sepIndex).trim()
    const rest = text.slice(sepIndex + 1).trim()
    i += 1
    if (rest === '') {
      // 裸 "key:"：其后更深缩进行为嵌套 map，否则值为 null（删键指令）
      if (i < lines.length && lines[i].indent > indent) {
        const [child, next] = buildYamlMap(lines, i, lines[i].indent)
        tree[key] = child
        i = next
      } else {
        tree[key] = null
      }
      continue
    }
    if (i < lines.length && lines[i].indent > indent) {
      throw new Error(`第 ${String(lines[i].lineNo)} 行语法错误：标量值下不允许再缩进`)
    }
    tree[key] = parseYamlScalar(rest, lineNo)
  }
  return [tree, i]
}

function parseYamlScalar(raw: string, lineNo: number): ConfigValue {
  if (raw === 'null' || raw === '~') {
    return null
  }
  if (raw === 'true') {
    return true
  }
  if (raw === 'false') {
    return false
  }
  if (/^-?\d+$/.test(raw)) {
    return Number.parseInt(raw, 10)
  }
  if (/^-?\d+\.\d+$/.test(raw)) {
    return Number.parseFloat(raw)
  }
  if (raw.startsWith('[') || raw.startsWith('{')) {
    try {
      return JSON.parse(raw) as ConfigValue
    } catch {
      throw new Error(`第 ${String(lineNo)} 行语法错误：行内列表 / 对象须为合法 JSON 形式`)
    }
  }
  if ((raw.startsWith('"') && raw.endsWith('"') && raw.length >= 2) || (raw.startsWith("'") && raw.endsWith("'") && raw.length >= 2)) {
    return raw.startsWith('"') ? (JSON.parse(raw) as string) : raw.slice(1, -1)
  }
  return raw
}

// ---- 规范序列化（固定键序，保证 hash 幂等） ----

/** 按固定键序规范序列化配置树 */
export function serializeConfig(format: ConfigFormat, tree: ConfigTree): string {
  if (format === 'json') {
    return JSON.stringify(sortTree(tree), null, 2)
  }
  if (format === 'properties') {
    return [...Object.keys(tree)]
      .sort()
      .map((key) => `${key}=${leafDisplay(tree[key])}`)
      .join('\n')
  }
  return serializeYamlMap(tree, 0)
}

/** 递归重建键序（JS 对象保持插入序，排序后 stringify 即确定性输出） */
function sortTree(value: ConfigValue): ConfigValue {
  if (Array.isArray(value)) {
    return value.map(sortTree)
  }
  if (isTree(value)) {
    const out: ConfigTree = {}
    for (const key of [...Object.keys(value)].sort()) {
      out[key] = sortTree(value[key])
    }
    return out
  }
  return value
}

function serializeYamlMap(tree: ConfigTree, depth: number): string {
  const pad = '  '.repeat(depth)
  const lines: string[] = []
  for (const key of [...Object.keys(tree)].sort()) {
    const value = tree[key]
    if (isTree(value)) {
      lines.push(`${pad}${key}:`)
      lines.push(serializeYamlMap(value, depth + 1))
    } else {
      lines.push(`${pad}${key}: ${yamlScalar(value)}`)
    }
  }
  return lines.filter((l) => l !== '').join('\n')
}

function yamlScalar(value: ConfigValue): string {
  if (value === null) {
    return 'null'
  }
  if (typeof value === 'object') {
    // 列表（map 已由上层递归处理，此分支兜底保证不落 [object Object]）
    return JSON.stringify(sortTree(value))
  }
  if (typeof value === 'string') {
    // 会被误解析成其他类型 / 含结构字符的字符串加引号，保证 parse 往返稳定
    const ambiguous =
      value === '' ||
      /^(null|~|true|false|-?\d+(\.\d+)?)$/.test(value) ||
      /[:#[\]{}"']/.test(value) ||
      value !== value.trim()
    return ambiguous ? JSON.stringify(value) : value
  }
  return String(value)
}

/** 叶子值的展示串（diff / properties 序列化共用） */
export function leafDisplay(value: ConfigValue | undefined): string {
  if (value === undefined) {
    return ''
  }
  if (value === null) {
    return 'null'
  }
  if (typeof value === 'object') {
    // 列表或 map：确定性键序 JSON 展示
    return JSON.stringify(sortTree(value))
  }
  if (typeof value === 'string') {
    return value
  }
  return String(value)
}

/** 摊平叶子路径 → 展示值（键级 diff 用）；叶子 = 标量或列表 */
export function flattenLeaves(tree: ConfigTree, prefix = ''): Map<string, string> {
  const out = new Map<string, string>()
  for (const key of [...Object.keys(tree)].sort()) {
    const path = prefix === '' ? key : `${prefix}.${key}`
    const value = tree[key]
    if (isTree(value)) {
      for (const [subPath, subValue] of flattenLeaves(value, path)) {
        out.set(subPath, subValue)
      }
    } else {
      out.set(path, leafDisplay(value))
    }
  }
  return out
}

// ---- 键级深合并 + provenance ----

/** 参与合并的一层输入（低 → 高顺序传入） */
export interface MergeLayerInput {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  versionNo: number
  tree: ConfigTree
}

export interface MergeOutcome {
  merged: ConfigTree
  provenance: ConfigProvenanceEntry[]
  deletedKeys: ConfigDeletedKey[]
}

/** 低 → 高逐层深合并：标量覆盖 / map 深合并 / list 整替 / null 删键，并记录逐叶子来源与删键 */
export function mergeLayerTrees(format: ConfigFormat, layers: MergeLayerInput[]): MergeOutcome {
  const merged: ConfigTree = {}
  const origin = new Map<string, ConfigProvenanceEntry>()
  const deletedKeys: ConfigDeletedKey[] = []
  // properties 无删键能力：值 "null" 是普通字符串，解析层已保证不出现 null
  const nullDeletes = format !== 'properties'

  const removeUnder = (path: string): void => {
    origin.delete(path)
    const prefix = `${path}.`
    for (const key of [...origin.keys()]) {
      if (key.startsWith(prefix)) {
        origin.delete(key)
      }
    }
  }

  // 记录 provenance：map 递归到叶子，标量 / 列表记路径本身
  const record = (path: string, value: ConfigValue, layer: MergeLayerInput): void => {
    if (isTree(value)) {
      for (const key of Object.keys(value)) {
        record(`${path}.${key}`, value[key], layer)
      }
      return
    }
    origin.set(path, {
      path,
      scopeLevel: layer.scopeLevel,
      scopeRefId: layer.scopeRefId,
      scopeName: layer.scopeName,
      versionNo: layer.versionNo,
    })
  }

  // 剥离 null（结果中不出现被删键）；整棵剥空返回 undefined 视同未提供
  const sanitize = (value: ConfigValue): ConfigValue | undefined => {
    if (value === null) {
      return nullDeletes ? undefined : null
    }
    if (isTree(value)) {
      const out: ConfigTree = {}
      for (const key of Object.keys(value)) {
        const sub = sanitize(value[key])
        if (sub !== undefined) {
          out[key] = sub
        }
      }
      return out
    }
    return value
  }

  const apply = (base: ConfigTree, overlay: ConfigTree, prefix: string, layer: MergeLayerInput): void => {
    for (const key of Object.keys(overlay)) {
      const path = prefix === '' ? key : `${prefix}.${key}`
      const value = overlay[key]
      if (value === null && nullDeletes) {
        // null 删键：低层存在才算删除并记录；结果中不出现该键
        if (key in base) {
          removeUnder(path)
          // 动态键删除是本合并引擎的既定语义
          // eslint-disable-next-line @typescript-eslint/no-dynamic-delete
          delete base[key]
          deletedKeys.push({
            path,
            scopeLevel: layer.scopeLevel,
            scopeRefId: layer.scopeRefId,
            scopeName: layer.scopeName,
            versionNo: layer.versionNo,
          })
        }
        continue
      }
      const current = base[key]
      if (isTree(value) && isTree(current)) {
        // map 深合并：递归按键合并
        apply(current, value, path, layer)
        continue
      }
      // 标量覆盖 / list 整体替换 / 类型不一致整体替换
      const sanitized = sanitize(value)
      if (sanitized === undefined) {
        continue
      }
      removeUnder(path)
      base[key] = sanitized
      record(path, sanitized, layer)
    }
  }

  for (const layer of layers) {
    apply(merged, layer.tree, '', layer)
  }
  return {
    merged,
    provenance: [...origin.values()].sort((a, b) => (a.path < b.path ? -1 : 1)),
    deletedKeys,
  }
}

// ---- 敏感路径读写（先按扁平键精确命中，再按 "." 分段嵌套导航，兼容三格式） ----

/** 读路径值：不存在返回 undefined */
export function getAtPath(tree: ConfigTree, path: string): ConfigValue | undefined {
  if (path in tree) {
    return tree[path]
  }
  const segments = path.split('.')
  let node: ConfigValue = tree
  for (const segment of segments) {
    if (!isTree(node) || !(segment in node)) {
      return undefined
    }
    node = node[segment]
  }
  return node
}

/** 写路径值：仅在路径已存在时覆盖，返回是否写入 */
export function setAtPath(tree: ConfigTree, path: string, value: ConfigValue): boolean {
  if (path in tree) {
    tree[path] = value
    return true
  }
  const segments = path.split('.')
  let node: ConfigValue = tree
  for (let i = 0; i < segments.length - 1; i++) {
    if (!isTree(node) || !(segments[i] in node)) {
      return false
    }
    node = node[segments[i]]
  }
  const last = segments[segments.length - 1]
  if (!isTree(node) || !(last in node)) {
    return false
  }
  node[last] = value
  return true
}

/** 深拷贝配置树 */
export function cloneTree(tree: ConfigTree): ConfigTree {
  return JSON.parse(JSON.stringify(tree)) as ConfigTree
}

/** 敏感路径脱敏：命中路径的标量叶子替换为占位符，返回新树 */
export function maskTree(tree: ConfigTree, sensitivePaths: readonly string[], placeholder: string): ConfigTree {
  if (sensitivePaths.length === 0) {
    return tree
  }
  const masked = cloneTree(tree)
  for (const path of sensitivePaths) {
    const value = getAtPath(masked, path)
    if (value !== undefined && value !== null && !isTree(value) && !Array.isArray(value)) {
      setAtPath(masked, path, placeholder)
    }
  }
  return masked
}
