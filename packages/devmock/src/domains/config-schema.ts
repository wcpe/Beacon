// 配置中心 V2 mock schema 校验器：JSON Schema Draft 2020-12 九关键字子集
// （type / properties / required / enum / minimum / maximum / pattern / items / additionalProperties，规格 §4.4）。
// 校验模式：**部分校验**——只校验提交内容中出现的键；required 完整性仅 namespace 层强制；
// 显式 null（删键指令）跳过类型校验。properties 格式按扁平键名匹配 properties 定义，
// 值均按 string 校验 pattern / enum。与 validate 端点、保存端点共用同一引擎。

import type { ConfigFormat } from '@beacon/contracts'
import { isTree, leafDisplay, type ConfigTree, type ConfigValue } from './config-engine'

/** 单条 schema 违例（对齐 CONFIG_SCHEMA_VIOLATION 的逐条 {path, message}） */
export interface SchemaIssue {
  path: string
  message: string
}

/** 支持的 schema 节点子集 */
export interface SchemaNode {
  type?: string | string[]
  properties?: Record<string, SchemaNode>
  required?: string[]
  enum?: (string | number | boolean | null)[]
  minimum?: number
  maximum?: number
  pattern?: string
  items?: SchemaNode
  additionalProperties?: boolean | SchemaNode
}

const KNOWN_TYPES = new Set(['object', 'array', 'string', 'number', 'integer', 'boolean', 'null'])

/** 校验 schema_json 本身是否为合法的子集 schema，非法返回中文错误描述，合法返回 null */
export function checkSchemaDefinition(schemaJson: string): string | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(schemaJson)
  } catch {
    return 'schemaJson 不是合法 JSON'
  }
  return checkSchemaNode(parsed, '(root)')
}

function checkSchemaNode(node: unknown, path: string): string | null {
  if (typeof node !== 'object' || node === null || Array.isArray(node)) {
    return `${path}：schema 节点必须是对象`
  }
  const s = node as Record<string, unknown>
  if (s.type !== undefined) {
    const types = Array.isArray(s.type) ? s.type : [s.type]
    for (const t of types) {
      if (typeof t !== 'string' || !KNOWN_TYPES.has(t)) {
        return `${path}：type 取值非法（${String(t)}）`
      }
    }
  }
  if (s.properties !== undefined) {
    if (typeof s.properties !== 'object' || s.properties === null || Array.isArray(s.properties)) {
      return `${path}：properties 必须是对象`
    }
    for (const [key, sub] of Object.entries(s.properties)) {
      const error = checkSchemaNode(sub, `${path}.${key}`)
      if (error !== null) {
        return error
      }
    }
  }
  if (s.required !== undefined && (!Array.isArray(s.required) || s.required.some((r) => typeof r !== 'string'))) {
    return `${path}：required 必须是字符串数组`
  }
  if (s.enum !== undefined && (!Array.isArray(s.enum) || s.enum.length === 0)) {
    return `${path}：enum 必须是非空数组`
  }
  if (s.minimum !== undefined && typeof s.minimum !== 'number') {
    return `${path}：minimum 必须是数字`
  }
  if (s.maximum !== undefined && typeof s.maximum !== 'number') {
    return `${path}：maximum 必须是数字`
  }
  if (s.pattern !== undefined) {
    if (typeof s.pattern !== 'string') {
      return `${path}：pattern 必须是字符串`
    }
    try {
      new RegExp(s.pattern)
    } catch {
      return `${path}：pattern 不是合法正则`
    }
  }
  if (s.items !== undefined) {
    const error = checkSchemaNode(s.items, `${path}.items`)
    if (error !== null) {
      return error
    }
  }
  if (s.additionalProperties !== undefined && typeof s.additionalProperties !== 'boolean') {
    const error = checkSchemaNode(s.additionalProperties, `${path}.additionalProperties`)
    if (error !== null) {
      return error
    }
  }
  return null
}

/** 按格式与层级对内容树执行部分校验，返回逐条违例（空数组 = 通过） */
export function validateContentAgainstSchema(
  format: ConfigFormat,
  schema: SchemaNode,
  tree: ConfigTree,
  enforceRequired: boolean,
): SchemaIssue[] {
  const issues: SchemaIssue[] = []
  if (format === 'properties') {
    validateFlat(schema, tree, enforceRequired, issues)
  } else {
    validateValue(tree, schema, '(root)', enforceRequired, issues)
  }
  return issues
}

function typeMatches(type: string, value: ConfigValue): boolean {
  switch (type) {
    case 'object':
      return isTree(value)
    case 'array':
      return Array.isArray(value)
    case 'string':
      return typeof value === 'string'
    case 'boolean':
      return typeof value === 'boolean'
    case 'integer':
      return typeof value === 'number' && Number.isInteger(value)
    case 'number':
      return typeof value === 'number'
    case 'null':
      return value === null
    default:
      return true
  }
}

function validateValue(
  value: ConfigValue,
  schema: SchemaNode,
  path: string,
  enforceRequired: boolean,
  issues: SchemaIssue[],
): void {
  // 显式 null 为删键指令，跳过类型与取值校验
  if (value === null) {
    return
  }
  if (schema.type !== undefined) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    if (!types.some((t) => typeMatches(t, value))) {
      issues.push({ path, message: `类型应为 ${types.join(' / ')}` })
      return
    }
  }
  if (schema.enum !== undefined && !isTree(value) && !Array.isArray(value)) {
    if (!schema.enum.includes(value)) {
      issues.push({ path, message: `取值须为 ${schema.enum.map(String).join(' / ')} 之一` })
    }
  }
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) {
      issues.push({ path, message: `不得小于 ${String(schema.minimum)}` })
    }
    if (schema.maximum !== undefined && value > schema.maximum) {
      issues.push({ path, message: `不得大于 ${String(schema.maximum)}` })
    }
  }
  if (typeof value === 'string' && schema.pattern !== undefined && !new RegExp(schema.pattern).test(value)) {
    issues.push({ path, message: `不匹配正则 ${schema.pattern}` })
  }
  if (Array.isArray(value) && schema.items !== undefined) {
    value.forEach((element, index) => {
      validateValue(element, schema.items ?? {}, `${path}[${String(index)}]`, enforceRequired, issues)
    })
  }
  if (isTree(value)) {
    if (enforceRequired && schema.required !== undefined) {
      for (const requiredKey of schema.required) {
        if (!(requiredKey in value)) {
          issues.push({ path: joinPath(path, requiredKey), message: '缺少必填键' })
        }
      }
    }
    for (const [key, child] of Object.entries(value)) {
      const childPath = joinPath(path, key)
      const childSchema = schema.properties?.[key]
      if (childSchema !== undefined) {
        validateValue(child, childSchema, childPath, enforceRequired, issues)
        continue
      }
      if (schema.additionalProperties === false) {
        issues.push({ path: childPath, message: 'schema 未定义该键（additionalProperties=false）' })
        continue
      }
      if (schema.additionalProperties !== undefined && schema.additionalProperties !== true) {
        validateValue(child, schema.additionalProperties, childPath, enforceRequired, issues)
      }
    }
  }
}

/** properties 格式：按扁平键名匹配 properties 定义，值均按 string 校验 pattern / enum */
function validateFlat(schema: SchemaNode, tree: ConfigTree, enforceRequired: boolean, issues: SchemaIssue[]): void {
  if (enforceRequired && schema.required !== undefined) {
    for (const requiredKey of schema.required) {
      if (!(requiredKey in tree)) {
        issues.push({ path: requiredKey, message: '缺少必填键' })
      }
    }
  }
  for (const [key, value] of Object.entries(tree)) {
    const prop = schema.properties?.[key]
    if (prop === undefined) {
      continue
    }
    const text = leafDisplay(value)
    if (prop.pattern !== undefined && !new RegExp(prop.pattern).test(text)) {
      issues.push({ path: key, message: `不匹配正则 ${prop.pattern}` })
    }
    if (prop.enum !== undefined && !prop.enum.map(String).includes(text)) {
      issues.push({ path: key, message: `取值须为 ${prop.enum.map(String).join(' / ')} 之一` })
    }
  }
}

function joinPath(path: string, key: string): string {
  return path === '(root)' ? key : `${path}.${key}`
}
