# ADR-0007：版本来源与发布渠道

**状态**：已接受

## 背景

Beacon 有三个可交付物（控制面 Go 二进制 / 镜像、两个 agent jar），需要统一的版本号来源与"稳定 / 快照"两条发布渠道，让 `sdd-release-version`、`sdd-publish-snapshot` 技能与 CI 都有一致依据，不各算各的。

## 决策

### 单一版本来源
- 仓库根 `VERSION` 文件是**唯一版本来源**，内容为 `MAJOR.MINOR.PATCH`（如 `0.1.0`），表示"当前开发、即将发布的目标版本"。
- 构建时注入各组件，三组件版本恒一致：
  - 控制面：`go build -ldflags "-X <pkg>/version.Version=$(cat VERSION)"`。
  - agent：Gradle 读取根 `VERSION` 作为 `project.version`。

### 两条发布渠道
- **稳定渠道**：在 `main` 上打 tag `vX.Y.Z`。CI 据 tag 构建，产物版本 = `X.Y.Z`，发 GitHub Release。
- **快照渠道**：`main` 每次推送（非 tag）。CI 构建开发版，产物版本 = `<VERSION>-SNAPSHOT+<short-sha>`；镜像 tag `:snapshot` 与 `:main-<short-sha>`，agent jar `*-SNAPSHOT.jar`；滚动发 `latest` 预发布（标注"开发构建、可能不稳定"）。

### 版本演进
- 开发期 `VERSION` 指向下一目标版本，main 快照即 `<VERSION>-SNAPSHOT`。
- 正式发布由 `sdd-release-version` 技能据提交内容判 SemVer、必要时更新 `VERSION`、打 `vX.Y.Z`；发布后把 `VERSION` 提到下一开发目标。
- 1.0.0 之前为 `0.y.z`，接口可不保证向后兼容（破坏性仍在 CHANGELOG 标明）。

## 后果

- CI、`sdd-release-version`、`sdd-publish-snapshot` 都以 `VERSION` + tag 为准。
- 快照可追溯到具体提交（`+<sha>`），稳定版与 tag 一一对应。
- 发快照与发正式版职责分离（分别对应两个技能）。

## 备选方案

- **纯 tag 驱动（`git describe`）不维护 VERSION**：多组件注入与"下一目标版本"表达不如显式 VERSION 清晰。未采用。
- **各组件各自维护版本号**：易漂移、对不齐。否决。
