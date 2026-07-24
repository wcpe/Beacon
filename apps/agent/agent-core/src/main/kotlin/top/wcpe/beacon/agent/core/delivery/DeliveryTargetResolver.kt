package top.wcpe.beacon.agent.core.delivery

import top.wcpe.beacon.agent.core.filesync.FileSyncPathGuard
import java.io.File
import java.io.IOException
import java.nio.file.Path

/**
 * 交付落盘目标路径解析与安全校验（FR-165）。
 *
 * 复用 [FileSyncPathGuard] 的服务器根内相对路径闸（拒 path traversal / 绝对路径 / 盘符 / 反斜杠 /
 * Windows 保留名 + 符号链接逃逸），并叠加**自我保护**：拒绝落在 agent 自身数据目录内的目标——
 * 交付备份 / 临时文件 / 身份文件都在 dataFolder 下，绝不能被交付覆盖（杜绝自我指涉与自毁）。
 *
 * @param serverRoot 服务器工作目录（agent 壳层：pluginsBase 的父目录），交付相对路径以此为根
 * @param dataDir    agent 自身数据目录（plugins/<agent 插件名>），整棵排除出可覆盖范围
 */
class DeliveryTargetResolver(
    serverRoot: File,
    dataDir: File,
) {
    private val guard = FileSyncPathGuard(serverRoot)
    private val dataDirReal: Path? = resolveRealOrNull(dataDir)

    /**
     * 解析并校验相对路径为服务器根内的落盘目标 [File]；非法 / 逃逸 / 落在 agent 自身目录内则返回 null。
     *
     * 目标可以尚不存在（add 项）：[FileSyncPathGuard] 以最近存在祖先的真实路径判定是否逃逸。
     */
    fun resolve(relPath: String): File? {
        val target = guard.resolveFile(relPath) ?: return null
        // 自我保护：目标落在 agent 数据目录内一律拒绝（交付绝不覆盖自身文件）。
        if (dataDirReal != null && target.startsWith(dataDirReal)) return null
        return target.toFile()
    }

    private fun resolveRealOrNull(dir: File): Path? =
        try {
            dir.toPath().toRealPath()
        } catch (e: IOException) {
            null
        }
}
