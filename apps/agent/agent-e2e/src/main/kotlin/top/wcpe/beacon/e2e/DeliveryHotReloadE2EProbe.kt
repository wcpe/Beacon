package top.wcpe.beacon.e2e

import taboolib.common.platform.function.getDataFolder
import taboolib.common.platform.function.info
import taboolib.common.platform.function.submit
import top.wcpe.beacon.agent.api.BeaconAgentProvider
import top.wcpe.beacon.agent.api.ListenerHandle
import java.io.File
import java.util.concurrent.atomic.AtomicLong

/**
 * FR-171 hot_reload 真机验收探针：作为业务插件订阅 BeaconAgent 配置变更回调，
 * 在回调当下读取真实磁盘内容并写观测；失败专用路径会在留证后抛错，用于验证生产失败回执与进程存活。
 */
object DeliveryHotReloadE2EProbe {
    const val SUCCESS_PATH = "plugins/BeaconE2E/delivery-hot-reload.yml"
    const val FAILURE_PATH = "plugins/BeaconE2E/delivery-hot-reload-fail.yml"
    const val OBSERVATION_FILE = "e2e-delivery-hot-reload.log"

    private const val INITIAL_CONTENT = "marker: original\n"
    private const val POLL_INTERVAL_TICKS = 20L
    private const val ALIVE_INTERVAL = 5L

    @Volatile
    private var handle: ListenerHandle? = null
    private val polls = AtomicLong(0)

    /** 清理上轮观测、种下两份原始配置并启动监听注册与存活探针。 */
    fun start() {
        val observation = File(getDataFolder(), OBSERVATION_FILE)
        if (observation.exists()) observation.delete()
        seed(SUCCESS_PATH)
        seed(FAILURE_PATH)
        E2EObservation.append(observation, "PROBE_START", "-", "-", "业务插件探针已启动")
        register(observation)
        submit(async = true, delay = POLL_INTERVAL_TICKS, period = POLL_INTERVAL_TICKS) {
            register(observation)
            if (polls.incrementAndGet() % ALIVE_INTERVAL == 0L) {
                E2EObservation.append(observation, "PROBE_ALIVE", "-", "-", "Paper 业务插件仍在线")
            }
        }
    }

    /** 注销业务插件监听器。 */
    fun stop() {
        handle?.remove()
        handle = null
    }

    /** Agent 就绪后注册真实 onChange；未就绪时由周期任务继续重试。 */
    private fun register(observation: File) {
        if (handle != null || !BeaconAgentProvider.isAvailable()) return
        handle =
            BeaconAgentProvider.get().config().onChange { changed, newMd5 ->
                for (path in listOf(SUCCESS_PATH, FAILURE_PATH)) {
                    if (changed.contains(path)) onChanged(observation, path, newMd5)
                }
            }
        E2EObservation.append(observation, "LISTENER_READY", "-", "-", "业务插件已订阅配置变更")
        info("Beacon E2E 已注册 FR-171 hot_reload 业务插件监听")
    }

    /** 在回调内读取落盘事实；失败路径留证后主动抛错，交由生产执行器形成 failed 回执。 */
    private fun onChanged(
        observation: File,
        path: String,
        newMd5: String,
    ) {
        val target = target(path)
        val raw = if (target.exists()) target.readText(Charsets.UTF_8) else "（目标文件不存在）"
        val md5 = if (target.exists()) E2EObservation.md5Hex(target.readBytes()) else newMd5
        E2EObservation.append(observation, "ON_CHANGE", path, md5, raw)
        if (path == FAILURE_PATH) {
            E2EObservation.append(observation, "CALLBACK_FAILED", path, md5, "业务插件拒绝本次热更新")
            error("FR-171 E2E 业务插件拒绝失败路径热更新")
        }
    }

    /** 仅当文件不存在时种原始内容，避免覆盖 Agent 已落盘事实。 */
    private fun seed(path: String) {
        val file = target(path)
        if (!file.exists()) {
            file.parentFile?.mkdirs()
            file.writeText(INITIAL_CONTENT, Charsets.UTF_8)
        }
    }

    /** 把固定服务器根相对路径映射到本业务插件数据目录。 */
    private fun target(path: String): File = File(getDataFolder(), path.substringAfterLast('/'))
}
