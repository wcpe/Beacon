package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import java.io.File
import java.util.concurrent.atomic.AtomicReference

/**
 * Beacon FR-14 文件树托管（通道B）的端到端验收探针（Bukkit 壳）。
 *
 * 职责：验证「控制面发布文件树文件 → agent 镜像落盘到插件真实 dataFolder → 业务插件读到镜像文件」。
 * 控制面侧发布 path 形如 `BeaconE2E/tree-managed.yml` 的文件树文件，镜像根为 plugins 基目录，
 * 故落到 plugins/BeaconE2E/tree-managed.yml——恰是本验收插件数据目录下的 [MIRROR_FILE]。
 * 本探针轮询该文件，出现 / 变更即把内容记到 e2e-filetree-observations.log，供外部驱动断言「插件读到镜像」。
 *
 * 注意：与覆盖探针（[OverrideE2EProbe]）分工——那条管「整文件覆盖 + 重载命令」，本条管「文件树镜像落盘」；
 * 二者观测各自独立的文件与标记文件，互不干扰。本探针只读磁盘 + 写自己的标记文件，不触碰 agent 落盘逻辑。
 */
object FileTreeE2EProbe {

    /** 文件树镜像落盘的目标文件名（相对本插件数据目录，即控制面 path 的末段）。 */
    private const val MIRROR_FILE = "tree-managed.yml"

    /** 文件树观测标记文件名：外部驱动据此断言。 */
    private const val OBSERVATION_FILE = "e2e-filetree-observations.log"

    /** 轮询周期（tick，20 tick/秒）：约每 1 秒读一次镜像文件现状。 */
    private const val POLL_INTERVAL_TICKS = 20L

    /** 上次观测到的镜像文件内容 md5，用于轮询去重（仅在「出现 / 内容变化」时记一条）。 */
    private val lastMd5 = AtomicReference<String?>(null)

    /**
     * 启动探针：清旧标记 + 删上轮镜像 → 启异步轮询观测 agent 本轮镜像落盘。
     * 由 BeaconE2EBukkit 在 ENABLE 阶段调用。
     */
    fun start() {
        val markFile = File(getDataFolder(), OBSERVATION_FILE)
        if (markFile.exists()) {
            markFile.delete()
        }
        // 删上轮镜像文件，确保观测的是 agent 本轮新落的镜像（applied-manifest 由编排侧一并复位）。
        File(getDataFolder(), MIRROR_FILE).delete()
        lastMd5.set(null)
        info("Beacon E2E 文件树探针已启用，观测镜像文件 $MIRROR_FILE，标记文件=${markFile.absolutePath}")

        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            val mirror = File(getDataFolder(), MIRROR_FILE)
            if (!mirror.exists()) {
                return@submit
            }
            val bytes = mirror.readBytes()
            val md5 = E2EObservation.md5Hex(bytes)
            if (lastMd5.getAndSet(md5) != md5) {
                E2EObservation.append(markFile, "FILE_TREE_MIRRORED", MIRROR_FILE, md5, String(bytes, Charsets.UTF_8))
            }
        }
    }
}
