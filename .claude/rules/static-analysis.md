# 代码风格与静态检查（防风格 / 质量漂移）

> 统一各组件的格式化与静态检查工具，并要求 CI 强制门禁——风格一致、低级问题挡在合并前。

## 1. 各组件工具链

- **控制面（Go）**：`gofmt` / `goimports` 格式化 + `golangci-lint`（配置见仓库根 `.golangci.yml`，含 govet / staticcheck / errcheck / ineffassign / revive / bodyclose / sqlclosecheck 等）。
- **前端（React / TS）**：`eslint` + `prettier`（配置在 `web/` 工程内）。
- **agent（Kotlin）**：`ktlint`（或 detekt）（配置在 `agent/` Gradle 工程内）。

## 2. 强制要求

- **CI 门禁**：lint 与格式检查未过 → 不允许合并（与测试同级，见 `testing-and-quality.md`）。
- **本地（强制，提交前必跑）**：**每次 `git commit` 前必须本地跑对应组件的 format + lint，绿了才提交**，绝不把格式 / 静态问题留给 CI。改了 Go → 跑 gofmt + go vet（+ golangci-lint 若已装）；改了前端 → `cd web && pnpm lint` / build；改了 agent → ktlint。"测试绿"不代表"格式 / lint 绿"——`go test` 不含 gofmt，必须单独跑。
- **依赖漏洞**：Go 侧用 `govulncheck` 作漏洞发现入口（零成本，纳入 CI）；升级流程见 `sdd-bump-dependencies`。
- 工具与规则版本固定（写进配置 / 构建），避免不同机器结果不一致。

### 2.1 本机 CRLF 陷阱与 CI-equivalent 校验命令（强制按此自检）

本仓 `.gitattributes`/CI 用 LF，但本机 `autocrlf=true` 使工作树为 CRLF——直接 `gofmt -l .` 会**全文件误报**，易让人误以为"格式问题都是 CRLF 噪声"而跳过，从而漏掉**真实** gofmt 问题（如 Go 1.19+ 文档注释列表后需空 `//` 行）。提交前按下法逐个校验改动的 Go 文件（去 CR 后再 gofmt，等价 CI）：

```bash
# 列出本批改动的 go 文件并逐个 CI-equivalent 校验（有输出=有真实格式问题）
for f in $(git diff --name-only HEAD -- '*.go'; git diff --name-only --cached -- '*.go'); do
  [ -f "$f" ] && { out=$(tr -d '\r' < "$f" | gofmt -d); [ -n "$out" ] && echo "❌ $f" && echo "$out"; }
done
go vet ./... && go build ./...
```

发现问题用 `gofmt` 输出的 diff 手动改源文件（**不要**直接 `gofmt -w`，会把整文件改成 LF 触发无关改动）。

## 3. 与现有规则的关系

- 本规则是 `testing-and-quality.md` 的补充：测试管"行为对不对"，静态检查管"写法干不干净"。
- 禁用某条 lint 要在**配置里集中声明并注明原因**，不在代码里零散 `//nolint` / `// eslint-disable` 关闭（除非有明确理由并写明）。
