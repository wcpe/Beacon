package top.wcpe.beacon.agent.core.command

import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.LinkOption
import java.nio.file.Path

/**
 * 读真实 plugins 目录树的 FS 边界（反向抓取，FR-39，见 ADR-0027）。纯 java.nio（非平台 API），core 可用；
 * 由平台壳的 [top.wcpe.beacon.agent.core.platform.PlatformAdapter.readPluginsTree] 用各自 plugins 根委托调用。
 *
 * 只读、不写盘；**仅在 async 线程调用**（读盘是阻塞 IO）。本类只管「安全地把字节读上来」，
 * 文本/二进制判别与上限是否超标由 core 纯函数 [PluginsTreeFilter] 判定（职责单一、可单测、为最终权威）。
 *
 * FS 级安全（限死真实 plugins 根内，ADR-0027 决策 3）：
 * - 以 root 的**真实规范化路径**（[Path.toRealPath]，解析符号链接后）为基准；
 * - 逐个候选文件解析其真实路径，**必须仍落在 root 真实路径之内**——根除符号链接逃逸（指向 root 外的链接被剔除）；
 * - 只收**普通文件**（跳过目录 / 目录符号链接 / 设备文件等非普通项），不跟随目录符号链接下降（防链接环）。
 *
 * 内存控制：
 * - 读盘前**按文件名跳过 `.jar`**——jar 是 plugins 目录最大的二进制来源（动辄数十 MB），按 ADR-0011 本就排除，
 *   不读其字节即免去最大 OOM 风险（[PluginsTreeFilter] 仍会按后缀再排除一次，双保险）；
 * - 单文件读取上限 `MAX_FILE_BYTES+1` 字节：超限文件只读到 1 字节溢出量，[PluginsTreeFilter] 即可判其超单文件上限
 *   而整体失败，绝不把超大文件全载进内存；
 * - 候选数达 `MAX_FILES+1` 即停止再收（已够纯函数判文件数超限拒绝；jar 已不计入，故不会因此漏掉本应保留的文本）。
 *   总字节不在此早停（避免「先读到的二进制把配额占满、漏读后面文本」的静默丢失）——靠跳 jar + 单文件上限把内存压住，
 *   真实 plugins 文本量通常远小于 64MB 总上限。
 */
object PluginsTreeReader {

    // 单文件最多读这么多字节：上限 + 1，让超限文件被 PluginsTreeFilter 判为「超单文件上限」而整体失败。
    private const val PER_FILE_READ_CAP: Long = PluginIngestLimits.MAX_FILE_BYTES + 1

    /**
     * 读取 [root]（真实 plugins 根）整棵子树为「相对路径（正斜杠） → 原始字节」映射。
     *
     * root 不存在 / 非目录返回空映射。单个文件读失败（权限等）跳过该文件、不中断整体（best-effort 读取）。
     *
     * @param root 真实 plugins 根目录（壳层传入：agent dataFolder 的父目录）
     */
    fun read(root: File): Map<String, ByteArray> {
        if (!root.isDirectory) return emptyMap()
        // 以 root 的真实路径（解析符号链接）为容纳基准；取不到（异常）则放弃整次读取（宁可不抓也不越界）。
        val rootReal: Path = try {
            root.toPath().toRealPath()
        } catch (e: IOException) {
            return emptyMap()
        }

        val result = LinkedHashMap<String, ByteArray>()
        // 候选数达上限+1 即停止再收（够 PluginsTreeFilter 判文件数超限）；jar 不计入，不致漏读本应保留的文本。
        var stopped = false

        walk(root) { file ->
            if (stopped) return@walk
            // 只收普通文件（跳目录 / 目录符号链接 / 非普通项）。
            if (!file.isFile) return@walk
            // 读盘前按名跳 jar（最大二进制来源，本就排除）：不读其字节，免最大 OOM 风险。
            if (file.name.lowercase().endsWith(".jar")) return@walk
            // FS 级符号链接逃逸判定：候选真实路径必须仍在 root 真实路径之内。
            val fileReal: Path = try {
                file.toPath().toRealPath()
            } catch (e: IOException) {
                return@walk // 解析失败（坏链接等）→ 跳过，不上传
            }
            if (!fileReal.startsWith(rootReal)) return@walk // 逃逸 root（符号链接指向外部）→ 剔除

            // 相对路径用 root 真实路径相对 file 真实路径，统一正斜杠（跨平台一致，供控制面再校验 / 落盘）。
            val relative = rootReal.relativize(fileReal).joinToString("/") { it.toString() }
            if (relative.isEmpty()) return@walk

            val bytes = readCapped(file) ?: return@walk
            result[relative] = bytes
            if (result.size > PluginIngestLimits.MAX_FILES) {
                stopped = true // 已超文件数上限，停止再收（纯函数会据此整体失败）
            }
        }
        return result
    }

    /**
     * scan 模式（FR-58，见 ADR-0037）：只 `stat` 取每个文件的字节大小为「相对路径（正斜杠） → size」映射，**不读内容**。
     *
     * 与 [read] 同样的 FS 级安全（root 真实路径容纳 + 符号链接逃逸剔除 + 跳目录/非普通项 + 按名跳 `.jar`），
     * 但**绝不读取任何文件字节、绝不因任何文件超大而失败**——超大运行时垃圾仅作为清单里 size 很大的一项被列出
     * （overThreshold 标注由 core 纯函数 [PluginsTreeFilter.scan] 据 size 判定）。无文件数早停（清单可大、永不失败）。
     *
     * root 不存在 / 非目录返回空映射。单个文件 `stat` 失败（权限等）跳过该文件、不中断整体（best-effort）。
     *
     * @param root 真实 plugins 根目录（壳层传入：agent dataFolder 的父目录）
     */
    fun readMetadata(root: File): Map<String, Long> {
        if (!root.isDirectory) return emptyMap()
        // 以 root 的真实路径（解析符号链接）为容纳基准；取不到（异常）则放弃整次读取（宁可不抓也不越界）。
        val rootReal: Path = try {
            root.toPath().toRealPath()
        } catch (e: IOException) {
            return emptyMap()
        }

        val result = LinkedHashMap<String, Long>()
        walk(root) { file ->
            // 只收普通文件（跳目录 / 目录符号链接 / 非普通项）。
            if (!file.isFile) return@walk
            // 按名跳 jar（最大二进制来源，本就排除）：scan 连其大小都不必列入（PluginsTreeFilter 仍会按后缀再排除一次）。
            if (file.name.lowercase().endsWith(".jar")) return@walk
            // FS 级符号链接逃逸判定：候选真实路径必须仍在 root 真实路径之内。
            val fileReal: Path = try {
                file.toPath().toRealPath()
            } catch (e: IOException) {
                return@walk // 解析失败（坏链接等）→ 跳过，不列入
            }
            if (!fileReal.startsWith(rootReal)) return@walk // 逃逸 root（符号链接指向外部）→ 剔除

            // 相对路径用 root 真实路径相对 file 真实路径，统一正斜杠（跨平台一致，供控制面再校验）。
            val relative = rootReal.relativize(fileReal).joinToString("/") { it.toString() }
            if (relative.isEmpty()) return@walk
            // 只 stat 取大小，绝不读内容；length() 失败（被删/权限）回 0，仍列入（清单宁可多列不漏）。
            result[relative] = file.length()
        }
        return result
    }

    /**
     * 深度优先遍历 root 下所有项，对每个**文件系统项**回调 [onFile]（含目录，由回调自行判类型）。
     *
     * 不跟随目录符号链接下降（[LinkOption.NOFOLLOW_LINKS] 判定目录性），避免符号链接环导致无限递归；
     * 指向外部的文件符号链接在上层按真实路径容纳判定剔除。
     */
    private fun walk(root: File, onFile: (File) -> Unit) {
        val stack = ArrayDeque<File>()
        stack.addLast(root)
        while (stack.isNotEmpty()) {
            val dir = stack.removeLast()
            val children = dir.listFiles() ?: continue
            for (child in children) {
                val isDir = Files.isDirectory(child.toPath(), LinkOption.NOFOLLOW_LINKS)
                if (isDir) {
                    stack.addLast(child)
                } else {
                    onFile(child)
                }
            }
        }
    }

    /** 读文件内容，最多 [PER_FILE_READ_CAP] 字节；读失败返回 null（跳过该文件）。 */
    private fun readCapped(file: File): ByteArray? {
        return try {
            file.inputStream().use { input ->
                val buffer = java.io.ByteArrayOutputStream()
                val chunk = ByteArray(8192)
                var remaining = PER_FILE_READ_CAP
                while (remaining > 0) {
                    val toRead = minOf(chunk.size.toLong(), remaining).toInt()
                    val n = input.read(chunk, 0, toRead)
                    if (n < 0) break
                    buffer.write(chunk, 0, n)
                    remaining -= n
                }
                buffer.toByteArray()
            }
        } catch (e: IOException) {
            null
        }
    }
}
