package top.wcpe.beacon.agent.core.filesync

import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.LinkOption
import java.nio.file.Path

/**
 * 文件同步服务器根路径安全校验。
 *
 * 同步目录与文件都必须是服务器根内的相对路径；拒绝 path traversal、绝对路径、盘符 / UNC、
 * 反斜杠、Windows 保留名，并用真实路径校验阻止符号链接逃逸。
 */
class FileSyncPathGuard(
    serverRoot: File,
) {
    private val rootReal: Path = serverRoot.toPath().toRealPath()

    /** 解析同步目录；非法或已存在路径逃逸时返回 null。 */
    fun resolveDirectory(relativeDir: String): Path? = resolve(relativeDir)

    /** 解析同步文件；非法或已存在路径逃逸时返回 null。 */
    fun resolveFile(relativePath: String): Path? = resolve(relativePath)

    private fun resolve(relativePath: String): Path? {
        if (!isSafeRelativePath(relativePath)) return null
        val target =
            try {
                rootReal.resolve(relativePath).normalize()
            } catch (e: Exception) {
                null
            }
        val safe =
            target != null &&
                target.startsWith(rootReal) &&
                target != rootReal &&
                nearestExistingPathStaysInRoot(target)
        return if (safe) target else null
    }

    private fun nearestExistingPathStaysInRoot(target: Path): Boolean {
        val existing = nearestExistingPath(target) ?: return false
        return realPathStaysInRoot(existing)
    }

    private fun nearestExistingPath(target: Path): Path? {
        var current: Path? = target
        while (current != null && !Files.exists(current, LinkOption.NOFOLLOW_LINKS)) {
            current = current.parent
        }
        return current
    }

    private fun realPathStaysInRoot(path: Path): Boolean {
        return try {
            path.toRealPath().startsWith(rootReal)
        } catch (e: IOException) {
            false
        }
    }

    companion object {
        /** 字符串级相对路径闸：先拒绝所有跨平台可绕过形态。 */
        fun isSafeRelativePath(path: String): Boolean {
            if (path.isEmpty()) return false
            if (path.startsWith('/') || path.contains('\\') || path.contains(':')) return false
            return path.split('/').all(::isSafeSegment)
        }

        private fun isSafeSegment(segment: String): Boolean {
            if (segment.isEmpty() || segment == "." || segment == "..") return false
            if (segment.trimEnd(' ', '.') != segment) return false
            return !isWindowsReserved(segment)
        }

        private fun isWindowsReserved(segment: String): Boolean {
            val base = segment.substringBefore('.').lowercase()
            return base in RESERVED_NAMES
        }

        /** Windows 保留设备名。 */
        private val RESERVED_NAMES: Set<String> =
            buildSet {
                addAll(listOf("con", "prn", "aux", "nul"))
                for (i in 1..9) {
                    add("com$i")
                    add("lpt$i")
                }
            }
    }
}
