# 注释规范

> 适用于仓库内所有源代码与配置文件（Go / Kotlin / TypeScript / Gradle KTS / YAML / 等）。

## 1. 强制规则

- **所有注释必须使用简体中文。** 禁止英文、日文、拼音等非中文语言。
- 包括但不限于：
  - Go 行注释 `//`、块注释 `/* */`、文档注释（包/导出标识符上方）
  - Kotlin/Java 的 `//`、`/* */`、KDoc/Javadoc `/** */`
  - TypeScript/React 的 `//`、`/* */`、JSDoc
  - `TODO` / `FIXME` / `NOTE` 标记后的说明文字
  - YAML / Properties / JSON5 / Dockerfile / compose 中的注释

## 2. 例外（不视为注释）

- 代码中的英文标识符（包名、类型名、函数名、变量名）。
- 字符串字面量、日志文案、用户可见文本。
- 第三方生成代码中的原始注释（应隔离在 `build/` / `dist/` 等非 VCS 目录）。
- 许可证头（如需保留英文版权声明可豁免）。

## 3. 示例

✅ 正确
```go
// 校验有效配置 md5，相同则挂起长轮询等待变更
func (h *Handler) effective(...) { ... }
```

❌ 错误
```go
// check md5 and hang if unchanged
func (h *Handler) effective(...) { ... }
```

## 4. 处理已有英文注释

- 改一处代码时，发现该处或紧邻位置有英文注释，可顺手译为中文。
- 禁止为"统一注释语言"对无关代码做大规模翻译式改动（精准修改原则）。
