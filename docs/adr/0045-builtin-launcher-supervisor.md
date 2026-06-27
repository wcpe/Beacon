# ADR-0045：内置 launcher 监督进程（独立第二二进制 + 退出码协议 + 跨平台换二进制）

**状态**：已被 [ADR-0053](0053-single-binary-self-replace.md) 取代

## 背景

Beacon 控制面是单二进制（Go + 内嵌前端）。生产形态下「进程崩了要自动拉起」「要换新二进制重启」通常依赖外部 systemd / docker restart 策略。但 Beacon 也支持**裸跑单二进制**（FR-25 开箱即跑），此时无任何外部监督——进程一旦崩溃就彻底停摆，且后续 FR-97「控制面在线自更新」需要一个**不被替换的常驻进程**来在主进程退出后换二进制、再拉起。

关键约束：**Windows 运行中的 `.exe` 文件被系统锁定，无法被自身进程覆盖**。因此「主进程内 fork 自监督 / 自换文件」在 Windows 上行不通——换文件必须由一个**不占用待换二进制的第三方进程**完成。

## 决策

1. **独立第二二进制 `beacon-launcher`**（`cmd/beacon-launcher`）作常驻监督进程，与主二进制同仓、版本经同一 `-ldflags -X internal/version.Version` 注入（[ADR-0007](0007-versioning-and-release-channels.md)，三组件版本恒一致）。它**极薄**：仅标准库 `os/exec` + `os`，不连 DB、不碰业务逻辑、不引第三方依赖。
2. **退出码协议**集中定义在单一文件 `internal/exitcode/exitcode.go`，主进程与 launcher **共享引用**（禁魔法数字散落）：
   - `0`（`OK`）= 主进程正常退出 → launcher 跟随退出、不重启。
   - `1`（`Crash`）及一切其它非零码（含信号退出 `128+signum`，如 SIGINT=130、SIGKILL=137）= 崩溃 / 启动失败 → 按**固定间隔**（3s）重启 + **连续失败计数上限**（5 次），超限即停并打 ERROR。**不用指数退避、不套 sysexits 语义**（简单优先）。
   - `70`（`RequestUpdateRestart`，项目内未占用值、命名常量）= 主进程请求更新重启 → launcher 用主进程已落位的 pending 新二进制做**原子替换后重启**。
3. **跨平台换二进制**（仅在子进程已退出后做）：
   - **类 Unix**：`rename` 同文件系统原子覆盖运行路径（旧 inode 进程已退出、自动 unlink）。
   - **Windows**：旧 `beacon.exe` rename 为 `beacon.old.exe` 让位 → pending（`beacon.new.exe`）rename 为 `beacon.exe` 就位 → 删旧。任一关键步失败尽力回滚旧二进制并报错。
   - 换失败 / pending 缺失 → **保留旧二进制、按旧版重启并打 WARN**（回滚兜底）。
4. **端口交接不做 socket fd 传递**（过度复杂）：主进程 graceful shutdown 释放端口 → 退出 → launcher 重启新进程重新监听同端口。亚秒级窗口内端口短暂不可用，由 agent **fail-static**（架构不变量：控制面不可用时 agent 按本地快照继续、绝不阻断玩家进服）兜底，玩家进服不受影响。
5. **本期 launcher 不自更新**：launcher 只负责「换主二进制 + 重启」机制；实际「查 Release / 下载 / 校验 / 落位 pending / 以 70 退出」的更新逻辑在 FR-97 实现。本期主进程侧仅把「请求更新重启」这条**退出出口与退出码常量准备好**（`cmd/beacon` 的 `errRequestUpdateRestart` 哨兵映射到 `exitcode.RequestUpdateRestart`），不实现任何更新动作。
6. **裸跑兜底**：无 launcher 直接跑 `beacon` 仍完全可用，只是退化为「无自动重启 / 无自更新」，需运维手动重启。launcher 是可选监督外壳，不是运行前提。

## 理由

- **独立进程是 Windows exe 文件锁的唯一正解**：换文件必须由不占用待换二进制的进程做，主进程内自换在 Windows 必失败。
- **退出码协议是最薄的进程间契约**：无需 IPC / socket / 共享内存，`os/exec` 拿子进程退出码即可决策；常量集中单文件 + 两端共享，杜绝魔法数字与两端语义漂移。
- **固定间隔 + 计数上限**足以防疯狂重启，指数退避 / sysexits 是过度设计（YAGNI）。
- **端口先退后起**避免 fd 传递的平台复杂度；亚秒窗口在已有 fail-static 下可接受。
- **launcher 极薄**保证监督进程本身极少出错、易审计；把更新复杂度留给 FR-97 的主进程侧。

## 后果

- 发布产物新增 `beacon-launcher[.exe]`（Makefile / release workflow / Docker ENTRYPOINT 适配在 FR-102 落地，本 ADR 仅定模型）。
- 主进程退出码语义被协议固定：`cmd/beacon` 的 `os.Exit` 路径统一走 `internal/exitcode` 常量。
- FR-97 在此模型上实现更新核心：落位 pending → 以 70 退出 → launcher 换二进制重启。
- 容器内自更新临时（镜像不可变），生产升级仍以重拉镜像为准（OPERATIONS 写明）。

## 被否的备选

- **在 `cmd/beacon/main.go` 内 fork 自监督 + 自换文件**：Windows 运行中 exe 文件锁导致自换必败；换文件必须由不被替换的进程做。否。
- **指数退避 / 套用 sysexits.h 语义**：超出需要的复杂度，固定间隔 + 计数上限已足够。否。
- **端口 socket fd 传递（零窗口交接）**：跨平台 fd 继承复杂、收益小（已有 fail-static 兜亚秒窗口）。否。
- **launcher 本期即实现自更新**：与 FR-97 职责重叠、把更新复杂度压进本应极薄的监督进程；本期 launcher 只备好换二进制机制。否。
