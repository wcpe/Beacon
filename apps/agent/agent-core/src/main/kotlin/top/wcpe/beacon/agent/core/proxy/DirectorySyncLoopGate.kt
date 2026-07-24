package top.wcpe.beacon.agent.core.proxy

import java.util.concurrent.atomic.AtomicReference

/** 代理目录同步递归链的单飞与代际门。 */
class DirectorySyncLoopGate {
    /** 每次成功启动生成的不可复用代际令牌。 */
    class Generation internal constructor()

    private val current = AtomicReference<Generation?>(null)

    /** 仅停止态可生成新代际并启动一条同步链。 */
    fun start(startLoop: (Generation) -> Unit): Generation? {
        val generation = Generation()
        if (!current.compareAndSet(null, generation)) return null
        startLoop(generation)
        return generation
    }

    /** 停止并立即失效当前代际。 */
    fun stop() {
        current.set(null)
    }

    /** 仅当前仍运行的同一代际返回 true。 */
    fun isCurrent(generation: Generation): Boolean = current.get() === generation
}
