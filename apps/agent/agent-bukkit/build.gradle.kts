import io.izzel.taboolib.gradle.Basic
import io.izzel.taboolib.gradle.Bukkit
import io.izzel.taboolib.gradle.BukkitHook
import io.izzel.taboolib.gradle.BukkitUtil
import io.izzel.taboolib.gradle.I18n

// agent-bukkit：以 TabooLib 形式运行在 Bukkit 子服的数据面插件，产出 BeaconAgent.jar。
// 装配 OkHttpTransport + KotlinxJsonCodec + 平台适配器，驱动 AgentLifecycle 接入控制面。
plugins {
    kotlin("jvm")
    id("io.izzel.taboolib")
}

// 先让依赖模块完成评估（应用各自的 kotlin 插件），再进入本壳的 afterEvaluate；
// 否则 TabooLib 在 afterEvaluate 中解析 project 依赖会触发依赖模块的 kotlin 插件
// 在评估锁定期 apply，抛 projectsEvaluated 的 IllegalMutationException。
evaluationDependsOn(":agent-core")
evaluationDependsOn(":agent-adapters")
evaluationDependsOn(":agent-api")
evaluationDependsOn(":agent-kit")

// 打进插件 jar 的仅本工程模块（agent 自有 core/adapters/api）；第三方库不打包，运行期由主类 @RuntimeDependencies 下载。
// 单独一条配置，便于在 jar 任务里精确 from(...)；显式排除 kotlin stdlib（TabooLib 运行期自带并重定位）。
val shadowed: Configuration by configurations.creating {
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-jdk8")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-jdk7")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-common")
}

dependencies {
    // 本工程模块：核心 + 适配器 + 对外 API + 便捷接入 kit，全部打进 jar（agent 自有代码）。
    // kit 必须随 api 一并打包，否则下游 compileOnly kit 运行期 NoClassDefFound。
    shadowed(project(":agent-core"))
    shadowed(project(":agent-adapters"))
    shadowed(project(":agent-api"))
    shadowed(project(":agent-kit"))

    // 编译期可见（不重复进 jar，由 shadowed 负责打包）。
    // 第三方 okhttp/kotlinx 不打包——改由插件主类 @RuntimeDependencies 运行期动态下载（参考 CoreLib）。
    compileOnly(project(":agent-core"))
    compileOnly(project(":agent-adapters"))
    compileOnly(project(":agent-api"))
    compileOnly(project(":agent-kit"))
    compileOnly(kotlin("stdlib"))
}

taboolib {
    // 插件元信息：展示名（最终 jar 名为 BeaconAgent）。
    description {
        name = "BeaconAgent"
        contributors {
            name("Beacon")
        }
    }
    // 锁定 TabooLib 版本。
    version { taboolib = "6.3.0-afd75a7" }
    env {
        // Bukkit 平台 + 配置模块（读取 config.yml）。
        // 配置模块在 TabooLib 6.2.x 的工件名为 basic-configuration。
        install(Basic)
        install(Bukkit)
        install(BukkitUtil)
        install(BukkitHook)
        install(I18n)
    }
    // 第三方库本身不打包（@RuntimeDependencies 运行期下载），但仍打包期 relocate：把 agent 自身字节码里
    // 对 okhttp3/okio/kotlinx.serialization 的引用重写到 top.wcpe.beacon.agent.lib.* —— 与运行期下载时的
    // relocate 目标一致，运行期下载的库即落到同一隔离命名空间，彼此及与 agent 互相可见。
    relocate("okhttp3", "${project.group}.lib.okhttp3")
    relocate("okio", "${project.group}.lib.okio")
    relocate("kotlinx.serialization", "${project.group}.lib.kotlinx.serialization")
}

// 产出 jar 基础名固定为 BeaconAgent，并把 shadowed 配置内的库与模块打进 jar，
// 供其后的 taboolibMainTask 做 relocate（taboolibMainTask 依赖 jar 输出原地重写）。
tasks.jar {
    archiveBaseName.set("BeaconAgent")
    // 显式依赖 shadowed 配置（含 agent-api/core/adapters 的 jar 产出），避免隐式依赖告警。
    dependsOn(shadowed)
    from(shadowed.map { if (it.isDirectory) it else zipTree(it) })
    duplicatesStrategy = DuplicatesStrategy.EXCLUDE
    exclude("META-INF/*.SF", "META-INF/*.DSA", "META-INF/*.RSA", "module-info.class")
}
