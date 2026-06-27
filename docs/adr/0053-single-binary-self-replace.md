# ADR-0053：控制面单进程二进制自替换 + 自动回滚（取代 launcher 双进程模型）

**状态**：已接受（取代 [ADR-0045](0045-builtin-launcher-supervisor.md)、部分取代 [ADR-0044](0044-control-plane-online-self-update.md) 决策 5/6/7）

## 背景

[ADR-0045](0045-builtin-launcher-supervisor.md) 引入独立第二二进制 `beacon-launcher` 作常驻监督进程，[ADR-0044](0044-control-plane-online-self-update.md) 在其上实现在线更新：主进程下载校验后落位 `beacon.new`、以退出码 `70` 退出，由 launcher 换二进制重启。两个 ADR 的核心论据是同一句**技术断言**：

> 「Windows 运行中的 `.exe` 文件被系统锁定，无法被自身进程覆盖 → 主进程内自换文件必败 → 换文件必须由一个不占用待换二进制的第三方进程（launcher）完成。」

这句断言**有误**，混淆了两个不同的文件操作：
- **覆盖**（写运行中的 exe 内容 / `O_TRUNC` 打开）：Windows 确实禁止（文件被映射执行、有共享锁）。
- **rename 让位**（把运行中的 exe 移到另一个名字）：Windows **允许**——重命名只改目录项，进程已打开的文件句柄仍指向原 inode，可执行映像不受影响。

因此「主进程把自己 `rename` 成 `beacon.old`、把新二进制放到 `beacon`、再 spawn 新进程、自己退出」在 Windows 上**完全可行**，无需第三方进程。姊妹项目 JianVideo 已用此「rename 让位三步」在单进程内稳定自替换（含跨卷 copy+sync 退化、删源前先关句柄等 Windows 细节），生产验证了可行性。

原 launcher 还提供「进程崩溃自动拉起」，但这在生产由 docker `restart` / systemd `Restart=` 提供，仅裸跑独有、而裸跑非生产形态。同时 launcher **没有**「新版换上去但起不来」的自动回退能力（换二进制后若新版崩溃，launcher 只会按崩溃码重启同一个坏新版，陷入崩溃循环）——这恰是在线更新最该防的风险。

## 决策

1. **去掉 `beacon-launcher`，主进程单进程自我替换**。删 `cmd/beacon-launcher/`（监督 + 跨平台换二进制）与 `internal/exitcode/`（退出码协议）。跨平台 rename 让位逻辑迁入 `internal/update`（按 `//go:build` 分平台）。
2. **自替换时序**：在线更新落位 `beacon.new`（沿用 [ADR-0044](0044-control-plane-online-self-update.md) 决策 1~4 的查/下/校验/落位）后，主进程**优雅关停 HTTP 释放端口** → `rename` 让位三步（`beacon`→`beacon.old`、`beacon.new`→`beacon`；Windows 含让位失败就地回退）→ spawn 新进程（继承参数/环境/工作目录/标准流）→ 旧进程退出。换二进制失败则就地回退、spawn 旧版继续服务（回退兜底）。**先关停再 spawn**，故无需 JianVideo 式的延时退出。
3. **自动回滚（崩溃循环闭合，本 ADR 新增、原方案无）**：换二进制成功后写 sentinel（`beacon.update-pending`，记 `attempt` 计数 + 目标版本）。新版**启动早期**（`run()` 入口、HTTP 起之前）自检：
   - sentinel 不存在 → 正常启动；
   - 存在 → `attempt++` 写回；`attempt >= maxStartAttempts`（默认 3）→ **自动回退**（`beacon`→`beacon.failed`、`beacon.old`→`beacon`、删 sentinel、spawn 旧版、退出）；否则启动**验证定时器**（存活过 `10s` 即删 sentinel + 删 `.old` 确认成功），继续启动。
   - **双路径确认成功**：① 验证定时器 10s；② 收到正常关停信号（SIGINT/SIGTERM，管理员或 docker stop 介入）也删 sentinel + `.old`。崩溃（非正常退出）两者都不触发 → sentinel 保留 → 外部监督重启时累加 attempt 至阈值 → 回退。
4. **崩溃自启交外部监督**：进程崩溃由 docker `restart: unless-stopped` / systemd `Restart=on-failure` 拉起（`docs/OPERATIONS.md` 写明推荐部署）。自动回滚依赖此重启来累加 attempt；裸跑无外部监督时新版崩溃即停（可接受），仍可靠下次手动启动触发自检回退。
5. **常量集中、不外部化**：`maxStartAttempts=3`、验证期 `10s`、sentinel/`.old`/`.failed` 文件名集中定义为常量，不做配置项（YAGNI）。`.old` 仅留 1 份。
6. **手动回滚（API + 前端）不在本 ADR**：属 FR-120，复用本 ADR 落地的 swap + `.old` 机制。

## 理由

- **rename 让位是 Windows exe「锁」的正解**：原 ADR 的「必须第三方进程」前提不成立；单进程 rename 自身既可行又消除第二个二进制与第二个进程，部署回归真正单文件。
- **先关停再 spawn 比延时退出更干净**：Beacon 已有优雅关停（等响应回包 + 释放端口），spawn 的新进程可立即绑定端口，无需 JianVideo 给 HTTP 回包预留的 800ms 延时窗口。
- **自动回滚补齐 launcher 的盲区**：launcher 能「换上去」却不能判断「起没起来」；sentinel + 启动自检 + attempt 阈值把「新版起不来」闭环成「几次后自动回退旧版」，这是在线更新真正需要的安全网，且不需要常驻监督进程。
- **双路径确认避免误判**：仅靠验证定时器会把「10s 内被管理员正常关停」误当崩溃累加；纳入正常关停信号作第二条成功路径，符合「能被正常操作=新版起来了」的直觉。
- **崩溃自启外置不丢能力**：生产本就用 docker/systemd；把崩溃重启交给它们既不重复造轮子，又让自动回滚搭其重启之便完成 attempt 累加。

## 后果

- 发布产物从「`beacon` + `beacon-launcher`」回归**单一 `beacon[.exe]`**；`Makefile`/`Dockerfile`/`_build-release.yml` 去 launcher 构建与上传，Docker `ENTRYPOINT` 从 `beacon-launcher` 改回 `beacon`（崩溃自启靠 compose `restart`）。
- `internal/update` 新增自替换子模块（swap + sentinel 自检 + 回退）；`cmd/beacon` 接「启动自检」与「触发自替换」两挂载点，去退出码 70 路径。
- [ADR-0044](0044-control-plane-online-self-update.md) 决策 5（原子落位仍保留）但决策 6（退出码交还 launcher）与决策 7 的「换了起不来由 launcher 兜」被本 ADR 取代为「主进程自替换 + 自动回滚」；其余决策（查 Release/下载/SHA256/进度内存态/审计）不变。
- `docs/OPERATIONS.md` 新增 systemd/docker restart 推荐部署小节；`docs/specs/online-update.md` 的 FR-96 章标过时。
- 真机自动回滚需以「起不来的新版」资产在 Win/Linux 验证，纳入 FR-119 验收。

## 被否的备选

- **保留 launcher、仅在 launcher 内补回滚**：仍是两个二进制两个进程，没解决部署冗余；且把「新版健康自检」塞进本应极薄的监督进程，违背其极薄定位。否。
- **照搬 JianVideo（仅手动回滚、无自动回退）**：「新版起不来」时手动 `/rollback` 端点也调不到（HTTP 起不来），崩溃循环无法闭合。否——必须有启动期自检自动回退。
- **sentinel 记时间戳而非 attempt 计数**：无法区分「首次换版启动」与「崩溃后第 N 次重启」，无法实现「崩 N 次回退」。否，用 attempt 计数。
- **把 `maxStartAttempts`/验证期做成配置项**：当前无人需要调，外部化是镀金；集中常量、需要时再提。否。
