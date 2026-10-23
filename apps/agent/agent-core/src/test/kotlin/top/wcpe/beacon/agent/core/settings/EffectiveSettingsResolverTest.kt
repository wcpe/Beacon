package top.wcpe.beacon.agent.core.settings

import top.wcpe.beacon.agent.core.identity.AgentCredentials
import top.wcpe.beacon.agent.core.identity.AgentCredentialsStore
import top.wcpe.beacon.agent.core.testsupport.TestJsonCodec
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.Path
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * [EffectiveSettingsResolver] 的完整回归锁（FR-276 #3）。
 *
 * ## 这条锁守的是「老部署不许变」
 *
 * 验收原文是「直接配好 config.yml（endpoints + token）→ 行为与现在逐字一致」。
 * 「逐字一致」不能靠人肉比对字段——解析器多引入一层（下发缓存 / 凭据合并）之后，
 * 任何一处把 config.yml 的值吃掉或改写，都可能被其它用例的宽松断言放过。
 * 因此这里的核心断言是**整体相等**：`resolve()` 必须与直接读 config.yml 的结果完全一致。
 *
 * ## 与 [EffectiveSettingsIdentityTest] 的分工
 *
 * 那一组覆盖「身份收敛 / 迁移」这条线（启动时写出一份只有身份、没有地址令牌的凭据）；
 * 本文件覆盖解析器本身的取舍：三层来源的优先级、凭据补齐、以及各种缺文件的降级形态。
 * 两者刻意不重复：那边断言「迁移不破坏接入」，这里断言「解析结果不因新链路而变形」。
 */
class EffectiveSettingsResolverTest {
    private fun tempDir(): Path = Files.createTempDirectory("beacon-settings-resolver")

    /**
     * config.yml 齐全 + 没有凭据文件 + 没有下发缓存 → 解析结果与直接读取**完全相同**。
     *
     * 「凭据文件不存在」是绝大多数老部署的真实形态（从未 join 过），
     * 这里显式断言它不影响任何字段，并且确实处于可接入状态。
     */
    @Test
    fun `configyml 配齐地址与令牌时解析结果与直接读取一致`() {
        val dir = tempDir()
        val local = legacyConfig()
        assertFalse(Files.exists(dir.resolve("agent-credentials.json")), "前提：本用例不创建凭据文件")

        val subject = resolver(dir, local)
        val resolved = subject.resolve()

        // 整体相等才是「逐字一致」：只比 endpoints/token 会漏掉其余 20 项被悄悄改写。
        assertEquals(AgentBootstrap.readSettings(local), resolved, "config.yml 齐全时解析结果必须与直接读取完全相同")
        assertEquals(listOf("https://beacon.example.com"), resolved.endpoints)
        assertEquals("bn_local", resolved.bootstrapToken)
        assertTrue(subject.hasConnectionConfig(resolved), "配齐了就必须判定为可接入")
    }

    /**
     * config.yml 齐全 **且** 凭据也可连接 → 仍以 config.yml 为准（凭据不得覆盖）。
     *
     * ## 为什么必须单独一条
     *
     * 上面两条用例都碰不到这条语义：一条没有凭据文件（`load()` 直接返回 null），
     * 一条的凭据不可连接（`canConnect=false`）。而「`/beacon join` 过的机器后来又手写了
     * config.yml」正是现实中最容易踩的形态——此时凭据与 config.yml 都可用，谁赢必须钉死。
     * 实测确认：砍掉这个判断（改成凭据无条件覆盖）时，只有本用例会红。
     *
     * 与 [EffectiveSettingsIdentityTest] 的同名场景刻意区分：那边走的是「身份迁移产物 +
     * 旧部署」，验证迁移不破坏接入；这里直接用可连接的凭据，验证优先级判定本身。
     */
    @Test
    fun `凭据可连接时也不得覆盖齐全的 configyml`() {
        val dir = tempDir()
        AgentCredentialsStore(dir, TestJsonCodec)
            .save(AgentCredentials(identityId = IDENTITY_ID, endpoint = "https://joined.example.com", token = "bn_joined"))
        val local = legacyConfig()

        val resolved = resolver(dir, local).resolve()

        assertEquals(listOf("https://beacon.example.com"), resolved.endpoints, "运维手写的地址必须优先于凭据")
        assertEquals("bn_local", resolved.bootstrapToken, "运维手写的令牌必须优先于凭据")
        assertEquals(AgentBootstrap.readSettings(local), resolved, "除身份外的一切都不该被凭据改写")
    }

    /**
     * config.yml 只写了 endpoints，token 走 join / 凭据补齐。
     *
     * ## 注意补齐是「整对替换」而不是「逐字段填空」
     *
     * 实现里凭据命中时用的是 `settings.copy(endpoints = listOf(credentials.endpoint), ...)`：
     * 地址与令牌**成对**取自凭据。这是刻意的——令牌只对它被签发的那套地址有效，
     * 把 config.yml 的地址和凭据的令牌拼在一起会得到一个永远鉴权失败的组合。
     * 本用例把该语义钉住，避免有人为「看起来更保守」改成逐字段补空。
     */
    @Test
    fun `只有 endpoints 时由凭据整对补齐地址与令牌`() {
        val dir = tempDir()
        AgentCredentialsStore(dir, TestJsonCodec)
            .save(AgentCredentials(identityId = IDENTITY_ID, endpoint = "https://joined.example.com", token = "bn_joined"))
        val local = MapConfigReader(mapOf("beacon.endpoints" to listOf("https://beacon.example.com")))

        val subject = resolver(dir, local)
        val resolved = subject.resolve()

        assertEquals(listOf("https://joined.example.com"), resolved.endpoints, "地址与令牌必须成对取自凭据")
        assertEquals("bn_joined", resolved.bootstrapToken)
        assertTrue(subject.hasConnectionConfig(resolved), "补齐后必须判定为可接入")
    }

    /** 全空（零配置、未 join）：只提示不报错——这是正常状态，不是配置错误。 */
    @Test
    fun `全空时为未接入状态且只提示不报错`() {
        val dir = tempDir()
        val warns = mutableListOf<String>()
        val infos = mutableListOf<String>()
        val local = MapConfigReader(emptyMap())

        val subject = resolver(dir, local, warns, infos)
        val resolved = subject.resolve()

        assertEquals(emptyList(), resolved.endpoints, "零配置下不该凭空出现地址")
        assertEquals("", resolved.bootstrapToken)
        assertFalse(subject.hasConnectionConfig(resolved), "未接入不该进入重连循环")
        assertTrue(infos.any { it.contains("尚未接入 Beacon") }, "应有一条指引（/beacon join 或 config.yml），实际：$infos")
        assertTrue(warns.isEmpty(), "未接入不是错误，不得打警告让人以为装坏了，实际：$warns")
    }

    /**
     * 凭据文件损坏 → 降级到 config.yml，**不抛异常**。
     *
     * 凭据是接入的根，写坏后最坏的结果只能是「回到只有 config.yml 的老形态」，
     * 绝不能让解析这一跳把 agent 起不来（那样一台手滑的文件就能砖掉整台服）。
     */
    @Test
    fun `凭据文件损坏时降级到 configyml 且不抛异常`() {
        val dir = tempDir()
        Files.writeString(dir.resolve("agent-credentials.json"), "{ 这不是合法 JSON", StandardCharsets.UTF_8)
        val local = legacyConfig()

        val subject = resolver(dir, local)
        val resolved = subject.resolve()

        assertEquals(AgentBootstrap.readSettings(local), resolved, "凭据读不出来时必须完整退回 config.yml")
        assertTrue(subject.hasConnectionConfig(resolved))
    }

    /**
     * 凭据里的地址/令牌为空（身份迁移的产物）→ 视为「无凭据」，不得覆盖 config.yml。
     *
     * 与 [EffectiveSettingsIdentityTest] 的用例不同的是：这里直接用凭据存储写出那份
     * 「只有身份」的文件，验证的是**解析器**而不是迁移流程。
     */
    @Test
    fun `只有身份没有地址令牌的凭据不得覆盖 configyml`() {
        val dir = tempDir()
        AgentCredentialsStore(dir, TestJsonCodec).save(AgentCredentials(identityId = IDENTITY_ID, endpoint = "", token = ""))
        val local = legacyConfig()

        val resolved = resolver(dir, local).resolve()

        assertEquals(AgentBootstrap.readSettings(local), resolved, "不可连接的凭据必须被整份忽略")
    }

    /** 组装解析器；日志出口默认丢弃，需要断言的用例自行传入。 */
    private fun resolver(
        path: Path,
        local: ConfigReader,
        warns: MutableList<String> = mutableListOf(),
        infos: MutableList<String> = mutableListOf(),
    ): EffectiveSettingsResolver =
        EffectiveSettingsResolver(
            localReader = local,
            pushedStore = PushedConfigStore(path, TestJsonCodec),
            credentialsStore = AgentCredentialsStore(path, TestJsonCodec),
            warn = warns::add,
            info = infos::add,
        )

    /** 老部署形态：config.yml 手写地址与令牌（也是「逐字一致」的比对基准）。 */
    private fun legacyConfig(): ConfigReader =
        MapConfigReader(
            mapOf(
                "beacon.endpoints" to listOf("https://beacon.example.com"),
                "beacon.bootstrap-token" to "bn_local",
            ),
        )

    /** 测试用的最小 ConfigReader：只回答 map 里写了的键，其余一律返回默认（= 没写）。 */
    private class MapConfigReader(
        private val values: Map<String, Any?>,
    ) : ConfigReader {
        override fun string(
            path: String,
            default: String,
        ): String = values[path] as? String ?: default

        override fun int(
            path: String,
            default: Int,
        ): Int = (values[path] as? Number)?.toInt() ?: default

        override fun long(
            path: String,
            default: Long,
        ): Long = (values[path] as? Number)?.toLong() ?: default

        override fun double(
            path: String,
            default: Double,
        ): Double = (values[path] as? Number)?.toDouble() ?: default

        override fun boolean(
            path: String,
            default: Boolean,
        ): Boolean = values[path] as? Boolean ?: default

        override fun stringList(path: String): List<String> = (values[path] as? List<*>)?.filterIsInstance<String>() ?: emptyList()

        override fun keys(path: String): Set<String> = emptySet()
    }

    /**
     * 出厂模板的占位符必须被当成「未配置」，而不是「运维已显式填好」。
     *
     * 真机实测暴露：出厂 config.yml 写的是 `<CONTROL_PLANE_URL>` / `<NAMESPACE_ACCESS_TOKEN>`，
     * 两者都非空 → 被判成「已接入」→ ① `/beacon join` 写入的凭据被永远盖住（join 静默失效）；
     * ② agent 对着不存在的地址无限重连；③ 占位符还会被当种子写进凭据文件，
     * 之后清空 config.yml 也救不回来。
     */
    @Test
    fun `模板占位符不构成接入配置`() {
        val dir = tempDir()
        val warns = mutableListOf<String>()
        // 用真实 config.yml 的形态（占位符）走完整解析，而不是手搓 AgentSettings。
        val subject =
            resolver(
                dir,
                MapConfigReader(
                    mapOf(
                        "beacon.endpoints" to listOf("<CONTROL_PLANE_URL>"),
                        "beacon.bootstrap-token" to "<NAMESPACE_ACCESS_TOKEN>",
                    ),
                ),
                warns = warns,
            )

        assertFalse(subject.hasConnectionConfig(subject.resolve()), "占位符不能算已配置")
        assertTrue(warns.any { it.contains("占位符") }, "必须明确告警是占位符，实际：$warns")
    }

    /** 真的填了地址与令牌才算已配置（回归锁：别把正常配置也拦掉）。 */
    @Test
    fun `真实地址与令牌构成接入配置`() {
        val dir = tempDir()
        val subject = resolver(dir, legacyConfig())
        assertTrue(subject.hasConnectionConfig(subject.resolve()))
    }

    /** 占位符绝不能作为凭据种子写进 agent-credentials.json。 */
    @Test
    fun `占位符不会作为凭据种子`() {
        val (phEndpoint, phToken) =
            EffectiveSettingsResolver.connectionSeed(
                listOf("<CONTROL_PLANE_URL>"),
                "<NAMESPACE_ACCESS_TOKEN>",
            )
        assertEquals("", phEndpoint, "占位符地址不得进入凭据")
        assertEquals("", phToken, "占位符令牌不得进入凭据")

        val (okEndpoint, okToken) =
            EffectiveSettingsResolver.connectionSeed(listOf("https://beacon.example.com"), "bn_local")
        assertEquals("https://beacon.example.com", okEndpoint)
        assertEquals("bn_local", okToken)
    }

    private companion object {
        /** 合法 UUIDv4：凭据解析要求身份标识必须是 v4，随便编一个会被判整份无效。 */
        const val IDENTITY_ID = "0f8fad5b-d9cb-469f-a165-70867728950e"
    }
}
