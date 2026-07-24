// 配置中心合并引擎与 schema 校验器单测：合并语义穷举（标量覆盖 / map 深合并 / list 整替 /
// null 删键 / properties 无删键）、规范序列化幂等、递归 provenance / deletedKeys、
// 敏感路径读写、schema 九关键字子集（部分校验 + required 仅 namespace 层 + null 跳过）。
import { describe, expect, it } from 'vitest'

import {
  flattenLeaves,
  getAtPath,
  maskTree,
  mergeLayerTrees,
  parseConfig,
  serializeConfig,
  setAtPath,
  type ConfigTree,
  type MergeLayerInput,
} from '../domains/config-engine'
import { checkSchemaDefinition, validateContentAgainstSchema, type SchemaNode } from '../domains/config-schema'

function layerOf(
  scopeLevel: MergeLayerInput['scopeLevel'],
  scopeRefId: number,
  scopeName: string,
  versionNo: number,
  tree: ConfigTree,
): MergeLayerInput {
  return { scopeLevel, scopeRefId, scopeName, versionNo, tree }
}

describe('解析与规范序列化', () => {
  it('yaml 嵌套 map 任意层级 + flow 列表解析，序列化往返幂等', () => {
    const content = 'database:\n  host: 10.30.0.6\n  pool:\n    min: 2\n    max: 10\nlist: [1, 2, 3]\nname: 金币'
    const tree = parseConfig('yaml', content)
    expect(tree).toEqual({
      database: { host: '10.30.0.6', pool: { min: 2, max: 10 } },
      list: [1, 2, 3],
      name: '金币',
    })
    const serialized = serializeConfig('yaml', tree)
    // 往返幂等：serialize(parse(serialize(x))) === serialize(x)（hash 幂等的基础）
    expect(serializeConfig('yaml', parseConfig('yaml', serialized))).toBe(serialized)
  })

  it('yaml 键序不同的等价内容序列化结果一致', () => {
    const a = serializeConfig('yaml', parseConfig('yaml', 'b: 2\na: 1'))
    const b = serializeConfig('yaml', parseConfig('yaml', 'a: 1\nb: 2'))
    expect(a).toBe(b)
  })

  it('yaml 语法错误逐类拒绝（缺冒号 / Tab 缩进 / 块序列 / 标量下缩进）', () => {
    expect(() => parseConfig('yaml', 'no-colon-here')).toThrow(/缺少 ":"/)
    expect(() => parseConfig('yaml', 'a:\n\tb: 1')).toThrow(/Tab/)
    expect(() => parseConfig('yaml', 'list:\n  - a')).toThrow(/块序列/)
    expect(() => parseConfig('yaml', 'a: 1\n  b: 2')).toThrow(/标量值下不允许再缩进/)
  })

  it('yaml 裸 "key:" 无子行时为 null（删键指令）', () => {
    expect(parseConfig('yaml', 'a:')).toEqual({ a: null })
  })

  it('json 全语义解析；顶层非对象拒绝', () => {
    expect(parseConfig('json', '{"a": {"b": [1, "x"]}, "c": null}')).toEqual({ a: { b: [1, 'x'] }, c: null })
    expect(() => parseConfig('json', '[1]')).toThrow(/顶层必须是对象/)
    expect(() => parseConfig('json', '{bad')).toThrow(/JSON 语法错误/)
  })

  it('properties 扁平键值，值一律字符串（"null" 是普通字符串）', () => {
    expect(parseConfig('properties', 'a=1\nb=null')).toEqual({ a: '1', b: 'null' })
    expect(() => parseConfig('properties', 'no-equals')).toThrow(/缺少 "="/)
  })

  it('flattenLeaves 摊平嵌套叶子路径（列表视为叶子）', () => {
    const tree = parseConfig('yaml', 'db:\n  host: h\n  pool:\n    min: 2\nlist: [1, 2]')
    expect([...flattenLeaves(tree).entries()]).toEqual([
      ['db.host', 'h'],
      ['db.pool.min', '2'],
      ['list', '[1,2]'],
    ])
  })
})

describe('键级深合并（规格 §4.1 穷举）', () => {
  it('标量覆盖：高层同键标量覆盖低层，provenance 记胜出层', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 2, { a: 1, b: 2 }),
      layerOf('zone', 30, 'area-1', 1, { a: 9 }),
    ])
    expect(outcome.merged).toEqual({ a: 9, b: 2 })
    expect(outcome.provenance).toEqual([
      { path: 'a', scopeLevel: 'zone', scopeRefId: 30, scopeName: 'area-1', versionNo: 1 },
      { path: 'b', scopeLevel: 'namespace', scopeRefId: 1, scopeName: 'prod', versionNo: 2 },
    ])
  })

  it('map 深合并：同键两侧均为 map 时递归按键合并', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 1, { db: { host: 'h', port: 3306 } }),
      layerOf('server', 1002, 'lobby-1', 1, { db: { port: 3307 } }),
    ])
    expect(outcome.merged).toEqual({ db: { host: 'h', port: 3307 } })
    const portOrigin = outcome.provenance.find((p) => p.path === 'db.port')
    const hostOrigin = outcome.provenance.find((p) => p.path === 'db.host')
    expect(portOrigin?.scopeLevel).toBe('server')
    expect(hostOrigin?.scopeLevel).toBe('namespace')
  })

  it('list 整体替换：任一侧为 list 时高层整体替换，不做元素级合并', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 1, { list: [1, 2, 3] }),
      layerOf('zone', 30, 'area-1', 1, { list: [9] }),
    ])
    expect(outcome.merged).toEqual({ list: [9] })
    expect(outcome.provenance).toEqual([
      { path: 'list', scopeLevel: 'zone', scopeRefId: 30, scopeName: 'area-1', versionNo: 1 },
    ])
  })

  it('类型不一致（map vs 标量）按整体替换处理，旧子树 provenance 清除', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 1, { node: { deep: 1 } }),
      layerOf('zone', 30, 'area-1', 1, { node: 'flat' }),
    ])
    expect(outcome.merged).toEqual({ node: 'flat' })
    expect(outcome.provenance).toEqual([
      { path: 'node', scopeLevel: 'zone', scopeRefId: 30, scopeName: 'area-1', versionNo: 1 },
    ])
  })

  it('null 删键：删除低层键并记录 deletedKeys（含执行层与名称）；结果不含 null', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 3, { a: 1, b: 2 }),
      layerOf('server', 1002, 'lobby-1', 2, { b: null }),
    ])
    expect(outcome.merged).toEqual({ a: 1 })
    expect(outcome.deletedKeys).toEqual([
      { path: 'b', scopeLevel: 'server', scopeRefId: 1002, scopeName: 'lobby-1', versionNo: 2 },
    ])
  })

  it('嵌套 null 删键：删嵌套键并保留兄弟键', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 1, { db: { pass: 'x', host: 'h' } }),
      layerOf('zone', 30, 'area-1', 1, { db: { pass: null } }),
    ])
    expect(outcome.merged).toEqual({ db: { host: 'h' } })
    expect(outcome.deletedKeys.map((k) => k.path)).toEqual(['db.pass'])
  })

  it('低层不存在的键收到 null 不记 deletedKeys、结果亦不出现', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 1, { a: 1 }),
      layerOf('zone', 30, 'area-1', 1, { ghost: null }),
    ])
    expect(outcome.merged).toEqual({ a: 1 })
    expect(outcome.deletedKeys).toEqual([])
  })

  it('删后更高层重加：键以更高层值回归', () => {
    const outcome = mergeLayerTrees('yaml', [
      layerOf('namespace', 1, 'prod', 1, { a: 1 }),
      layerOf('zone', 30, 'area-1', 1, { a: null }),
      layerOf('server', 1002, 'lobby-1', 1, { a: 5 }),
    ])
    expect(outcome.merged).toEqual({ a: 5 })
    expect(outcome.provenance).toEqual([
      { path: 'a', scopeLevel: 'server', scopeRefId: 1002, scopeName: 'lobby-1', versionNo: 1 },
    ])
  })

  it('properties 无删键：值 "null" 是普通字符串、原样覆盖保留', () => {
    const outcome = mergeLayerTrees('properties', [
      layerOf('namespace', 1, 'prod', 1, parseConfig('properties', 'a=1\nb=2')),
      layerOf('zone', 31, 'area-2', 1, parseConfig('properties', 'b=null')),
    ])
    expect(outcome.merged).toEqual({ a: '1', b: 'null' })
  })
})

describe('敏感路径读写', () => {
  it('getAtPath / setAtPath：扁平键优先，其次按 "." 嵌套导航；不存在不写入', () => {
    const flatFirst: ConfigTree = { 'a.b': 1, a: { b: 2 } }
    expect(getAtPath(flatFirst, 'a.b')).toBe(1)
    const nested: ConfigTree = { db: { password: 'x' } }
    expect(getAtPath(nested, 'db.password')).toBe('x')
    expect(getAtPath(nested, 'db.missing')).toBeUndefined()
    expect(setAtPath(nested, 'db.password', 'y')).toBe(true)
    expect(nested).toEqual({ db: { password: 'y' } })
    expect(setAtPath(nested, 'db.missing', 'z')).toBe(false)
  })

  it('maskTree：命中路径的标量叶子替换为占位符，不改原树；map / 缺失路径跳过', () => {
    const tree: ConfigTree = { db: { password: 'secret', host: 'h' }, sub: { m: { k: 1 } } }
    const masked = maskTree(tree, ['db.password', 'sub.m', 'ghost'], '__X__')
    expect(masked).toEqual({ db: { password: '__X__', host: 'h' }, sub: { m: { k: 1 } } })
    // 原树不被修改
    expect(tree.db).toEqual({ password: 'secret', host: 'h' })
  })
})

describe('schema 九关键字子集校验', () => {
  const schema: SchemaNode = {
    type: 'object',
    properties: {
      n: { type: 'integer', minimum: 1, maximum: 10 },
      mode: { type: 'string', enum: ['a', 'b'] },
      name: { type: 'string', pattern: '^[a-z]+$' },
      list: { type: 'array', items: { type: 'integer' } },
      nested: { type: 'object', properties: { flag: { type: 'boolean' } }, additionalProperties: false },
    },
    required: ['n', 'mode'],
  }

  it('checkSchemaDefinition：非法 JSON / 未知 type / 非法 pattern 拒绝，合法通过', () => {
    expect(checkSchemaDefinition('{bad')).toMatch(/不是合法 JSON/)
    expect(checkSchemaDefinition('{"type":"integerish"}')).toMatch(/type 取值非法/)
    expect(checkSchemaDefinition('{"pattern":"["}')).toMatch(/不是合法正则/)
    expect(checkSchemaDefinition('{"properties":{"a":{"required":"x"}}}')).toMatch(/required/)
    expect(checkSchemaDefinition(JSON.stringify(schema))).toBeNull()
  })

  it('类型 / 范围 / 枚举 / 正则 / items / additionalProperties 违例逐条报出', () => {
    const tree = parseConfig('json', '{"n": 99, "mode": "c", "name": "UPPER", "list": [1, "x"], "nested": {"flag": true, "extra": 1}}')
    const issues = validateContentAgainstSchema('json', schema, tree, false)
    const byPath = new Map(issues.map((i) => [i.path, i.message]))
    expect(byPath.get('n')).toMatch(/不得大于 10/)
    expect(byPath.get('mode')).toMatch(/取值须为 a \/ b/)
    expect(byPath.get('name')).toMatch(/不匹配正则/)
    expect(byPath.get('list[1]')).toMatch(/类型应为 integer/)
    expect(byPath.get('nested.extra')).toMatch(/additionalProperties=false/)
  })

  it('部分校验：只校验出现的键；required 仅 enforceRequired（namespace 层）时强制', () => {
    const partial = parseConfig('yaml', 'n: 5')
    expect(validateContentAgainstSchema('yaml', schema, partial, false)).toEqual([])
    const issues = validateContentAgainstSchema('yaml', schema, partial, true)
    expect(issues).toEqual([{ path: 'mode', message: '缺少必填键' }])
  })

  it('显式 null（删键指令）跳过类型校验', () => {
    const tree = parseConfig('yaml', 'n: null')
    expect(validateContentAgainstSchema('yaml', schema, tree, false)).toEqual([])
  })

  it('properties 格式按扁平键名校验 pattern / enum（值均按 string）', () => {
    const flatSchema: SchemaNode = {
      properties: {
        'max-players': { pattern: '^\\d+$' },
        difficulty: { enum: ['easy', 'hard'] },
      },
      required: ['max-players'],
    }
    const bad = parseConfig('properties', 'max-players=abc\ndifficulty=extreme')
    const issues = validateContentAgainstSchema('properties', flatSchema, bad, false)
    expect(issues.map((i) => i.path).sort()).toEqual(['difficulty', 'max-players'])
    // required 仅 namespace 层
    const missing = parseConfig('properties', 'difficulty=easy')
    expect(validateContentAgainstSchema('properties', flatSchema, missing, false)).toEqual([])
    expect(validateContentAgainstSchema('properties', flatSchema, missing, true)).toEqual([
      { path: 'max-players', message: '缺少必填键' },
    ])
  })
})
