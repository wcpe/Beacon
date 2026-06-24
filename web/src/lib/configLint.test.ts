// configLint 纯函数单测（FR-75）：客户端配置格式校验。
// 覆盖 JSON（JSON.parse）与 YAML（轻量结构校验：禁 Tab 缩进 / 引号 / 括号 / key 结构），
// 以及空内容放行、properties/plaintext 不校验。
import { describe, it, expect } from 'vitest'
import { lintContent } from './configLint'

describe('lintContent - JSON', () => {
  it('合法 JSON 返回 null', () => {
    expect(lintContent('json', '{"a": 1, "b": [1, 2]}')).toBeNull()
  })

  it('多余逗号的 JSON 报错', () => {
    const err = lintContent('json', '{"a": 1,}')
    expect(err).not.toBeNull()
    expect(err!.message).toBeTruthy()
    expect(err!.line).toBeGreaterThanOrEqual(1)
  })

  it('缺引号的 JSON 报错', () => {
    const err = lintContent('json', '{a: 1}')
    expect(err).not.toBeNull()
  })

  it('多行 JSON 错误尽量定位到出错行', () => {
    const err = lintContent('json', '{\n  "a": 1\n  "b": 2\n}')
    expect(err).not.toBeNull()
    expect(err!.line).toBeGreaterThanOrEqual(1)
  })
})

describe('lintContent - YAML', () => {
  it('合法 YAML 返回 null', () => {
    expect(lintContent('yaml', 'a: 1\nb:\n  c: 2\nlist:\n  - x\n  - y\n')).toBeNull()
  })

  it('Tab 缩进的 YAML 报错并指向该行', () => {
    const err = lintContent('yaml', 'a:\n\tb: 1\n')
    expect(err).not.toBeNull()
    expect(err!.line).toBe(2)
  })

  it('引号未闭合的 YAML 报错', () => {
    const err = lintContent('yaml', "name: 'beacon\n")
    expect(err).not.toBeNull()
    // js-yaml 单引号标量跨行至 EOF 才发现未闭合，mark 指向流末（第 2 行）
    expect(err!.line).toBe(2)
  })

  it('括号未闭合的 YAML 报错', () => {
    const err = lintContent('yaml', 'list: [1, 2\n')
    expect(err).not.toBeNull()
  })

  it('注释与文档标记不误报', () => {
    expect(lintContent('yaml', '# 注释\n---\na: 1\n...\n')).toBeNull()
  })

  it('行内 # 注释不被当作未闭合引号', () => {
    expect(lintContent('yaml', "a: 1 # don't worry\n")).toBeNull()
  })

  it('缺冒号的映射行报错', () => {
    const err = lintContent('yaml', 'a: 1\nthis is not valid\n')
    expect(err).not.toBeNull()
    // js-yaml 在解析到流末才确定无法构成有效映射，mark 指向第 3 行
    expect(err!.line).toBe(3)
  })
})

describe('lintContent - 放行场景', () => {
  it('空内容放行', () => {
    expect(lintContent('yaml', '')).toBeNull()
    expect(lintContent('json', '   \n  ')).toBeNull()
  })

  it('properties 不做格式解析（放行）', () => {
    expect(lintContent('properties', 'a=1\nb: [unbalanced')).toBeNull()
  })

  it('plaintext 不做格式解析（放行）', () => {
    expect(lintContent('plaintext', '{ not json at all')).toBeNull()
  })
})
