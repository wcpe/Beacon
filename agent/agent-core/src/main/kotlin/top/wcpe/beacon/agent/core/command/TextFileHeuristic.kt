package top.wcpe.beacon.agent.core.command

/**
 * 按文件名后缀启发判定「是否疑似文本」的**纯函数**（无 IO、无副作用）。
 *
 * 反向抓取 scan（[PluginsTreeFilter.scan]）与只读浏览（FR-109，列目录 / 读子树标 isText）共用同一口径，
 * 避免二进制扩展名集合复制散落两处。命中常见二进制扩展名（图片 / 压缩 / 库 / 序列化数据等）判 false；
 * 其余一律 true（保守倾向文本，由真正读内容时再精判）。
 *
 * **不是安全边界**：仅作展示弱化提示。真正拦二进制内容的是读字节时的 UTF-8 解码 + NUL 判定。
 */
object TextFileHeuristic {

    /** 按文件名（或相对路径）后缀启发判定是否疑似文本。 */
    fun looksTextByName(path: String): Boolean {
        val lower = path.lowercase()
        val dot = lower.lastIndexOf('.')
        if (dot < 0) return true // 无扩展名（如 README）保守按文本
        val ext = lower.substring(dot + 1)
        return ext !in BINARY_EXTENSIONS
    }

    /** 常见二进制扩展名集合（不含点，小写）。 */
    private val BINARY_EXTENSIONS: Set<String> = setOf(
        // 归档 / 库
        "jar", "zip", "gz", "tar", "rar", "7z", "war",
        // 图片
        "png", "jpg", "jpeg", "gif", "bmp", "ico", "webp",
        // 序列化 / 数据库 / 区块数据
        "dat", "db", "mca", "mcr", "nbt", "bin", "ser",
        // 字体 / 音视频 / 可执行
        "ttf", "otf", "wav", "ogg", "mp3", "class", "so", "dll", "exe",
    )
}
