# 发布流程规范（防跳号与空壳候选）

> 依据 [ADR-0073](../../docs/adr/0073-standard-rc-ga-release-lifecycle.md)、[ADR-0092](../../docs/adr/0092-runtime-version-carries-candidate-suffix.md)。**发布 tag 由流水线创建，人不得代劳。**

## 1. 发布 tag 只能由流水线创建（强制）

- 发布 tag（`vX.Y.Z` / `vX.Y.Z-rc.N`）**只能**由 `.github/workflows/rc.yml`（候选）或 `.github/workflows/release.yml`（正式）创建。
- **禁止**先在本机 `git tag` 再 dispatch 发布流水线。rc.yml 的「创建唯一 RC tag」步骤按「现有 tag 最大序号 + 1」生成新编号，手工 tag 会把基数顶高，直接造成**跳号**与**无 Release 的空壳 tag**。
  - 实测（2026-10-20）：手工打 `v1.5.0-rc.5` 后 dispatch，流水线发布出 `v1.5.0-rc.6`；`rc.5` 只剩 tag、无 Release 无资产。
- 该步骤只拒绝「同一提交已有**已发布 Release**」的情况，**不拒绝只有 tag 没有 Release 的空壳**，因此它不会替你拦住手工 tag。

## 2. 动手前必须读完发布工作流

- 执行任何发布动作前，**完整读完** `rc.yml`（tag 命名、各门禁、产物汇总、Maven 发布全部段落），不得只读开头就操作。
- dispatch 前逐项核对：
  - `VERSION` 文件的值 == `expected_version` 入参；
  - 待发布提交就是远端 `master` 顶端（workflow 会校验，漂移即拒）；
  - 同提交存在**唯一**成功的 master CI run，且其「最终质量门禁」为 success；
  - 该 run 的四个平台 `beacon-ci-<sha>-*` artifact 齐全未过期；
  - 现有 RC 序号连续、无空壳 tag。

## 3. 编号连续，禁止跳号与重复

- RC 编号严格连续：`rc.1 → rc.2 → …`，不得跳号。
- **同一提交不允许存在两个 RC tag**（哪怕其中一个没有 Release）。
- 发布完成后必须校验：
  - 新 tag 序号 == 前一个序号 + 1；
  - 新 tag 有对应 Release，且资产完整：四个平台控制面 + `BeaconAgent-*.jar` + `BeaconAgentProxy-*.jar` + Maven SDK + `SHA256SUMS.txt`；
  - tag 指向的就是预期的发布提交。

## 4. 破坏性发布操作需先取得明确授权

- 删除 Release、删除远端 tag、覆盖已发布资产，属破坏性操作，**必须获得用户显式同意**后才执行。
- 一旦已造成空壳 tag 或跳号，修复方向是：删除错误 Release 与相关 tag → 让流水线从正确基数重新生成。**不得用改名、复用旧 tag、或「反正内容一样」等权宜手段掩盖跳号。**

## 5. 与现有规则的关系

- 发布提交本身遵循 [git-commit.md](git-commit.md)：`chore(release): 发布 X.Y.Z`，只承载版本、CHANGELOG 与必要文档。
- CHANGELOG 定稿口径见 [doc-sync.md](doc-sync.md)；本项目 GA 复用最终 RC 同字节资产，故候选阶段把新增条目折进目标版本段，**CHANGELOG 不出现 rc 编号**，候选标识只由 tag 与运行时版本号承载（ADR-0092）。
