# ADR-0061：静态检查最严档三线

**状态**：已接受（随 P1（0.21.x，FR-176）落地；落地时同步 `.claude/rules/static-analysis.md`）

## 背景

现行静态检查档位偏温和：Go 侧 `.golangci.yml` 只启用 v2 standard 默认集 + 4 个补充 linter；前端 ESLint 处于 recommended 档；agent 侧 detekt 带 baseline 运行。第二版在 P1 一次换好工程底座（ADR-0060），此后 P2 起的全部代码都是净新代码——这是把检查档位一次拉满的最低成本窗口：新代码从第一天就贴最严标准写，没有存量违例包袱。参考仓库 Vantaloom 经勘察实际为宽松档（ESLint recommended 且全部降 warn），不作为模板来源，本 ADR 自建三线配置。

## 决策

按语言三线，全部作为 **CI 门禁**（不过不合并），工具与规则版本固定进配置：

1. **TypeScript**：`packages/eslint-config` 落 typescript-eslint **`strictTypeChecked` + `stylisticTypeChecked`**（类型感知检查）+ prettier。违例即 error，不做全局降 warn。适用 `apps/web`、`apps/ui-wiki`、`packages/ui`、`packages/devmock`；Legacy `web/` 冻结不适用。
2. **Go**：`.golangci.yml` 改为**全量启用档**（golangci-lint v2 `default: all`），不适配项以**集中禁用清单**声明并逐条注明原因（预期禁用如 `depguard`、`varnamelen`、`exhaustruct`、`ireturn` 等主观风格类；清单在 FR-176 实施时定稿）。格式化维持 gofmt + goimports，CRLF 规避沿用 `make lint` 既有机制。
3. **Kotlin**：detekt **全规则集**（`buildUponDefaultConfig` + 全 ruleset 激活，含默认关闭规则），ktlint 照旧；存量代码走 baseline 豁免，**新代码零违例**、禁止扩充 baseline。
4. **豁免纪律**（沿用 static-analysis.md §3 并加严）：单点关闭（`//nolint` / `eslint-disable` / `@Suppress`）必须附一句原因注释；成规律的不适配改到集中配置里声明，不许零散铺开。

## 理由

- 检查档位的成本曲线随存量代码单调上升：P1 是第二版代码量最接近零的时刻，此时拉满档位的边际成本最低。
- "最严档 + 集中声明的禁用清单"比"温和档 + 逐步加规则"可审计：禁了什么、为什么禁，一个文件说清；加严路线则永远说不清"还差多少"。
- 三线统一进 CI 门禁与 `turbo run lint` / `make lint` 对齐（ADR-0060 的任务编排），本地与 CI 结果一致。

## 后果

- FR-176 实施时：重写 `.golangci.yml` 为全量启用档并定稿禁用清单（现有 Legacy Go 代码需要修到零违例或进入豁免声明——工作量在实施时评估，必要时对 Legacy 目录级豁免并注明）；`packages/eslint-config` 新建；agent detekt 配置切全规则并生成存量 baseline。
- `.claude/rules/static-analysis.md` 的档位描述与命令随 FR-176 同步更新（引用本 ADR）。
- 严档必然带来更多误报申诉成本，靠豁免纪律（附原因）而非降档消化。

## 备选方案

- **维持现档**：零成本，但与第二版"保证所有代码质量"的目标不符。否决。
- **逐步加严（每期加一批规则）**：每次加严都要回修存量，总成本更高且永远处于"半严"状态。否决。
- **采用 Vantaloom 模板**：勘察证实其为宽松档（全 warn），达不到要求。否决。
- **TS 用 `strict`（非 type-checked）档**：省去类型感知检查的构建耗时，但放走一整类空值/Promise 误用问题，与"最严"目标不符。否决。
