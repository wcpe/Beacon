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

// 需要打进插件 jar 的运行期库与本工程模块（okhttp/okio/kotlinx + core/adapters/api）。
// 单独一条配置，便于在 jar 任务里精确 from(...)；显式排除 kotlin stdlib（TabooLib 运行期自带并重定位）。
val shadowed: Configuration by configurations.creating {
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-jdk8")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-jdk7")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-common")
}

dependencies {
    // 本工程模块：核心 + 适配器 + 对外 API，全部打进 jar。
    shadowed(project(":agent-core"))
    shadowed(project(":agent-adapters"))
    shadowed(project(":agent-api"))
    // 第三方运行期库：随适配器进 jar。
    shadowed("com.squareup.okhttp3:okhttp:4.12.0")
    shadowed("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")

    // 编译期可见（不重复进 jar，由 shadowed 负责打包）。
    compileOnly(project(":agent-core"))
    compileOnly(project(":agent-adapters"))
    compileOnly(project(":agent-api"))
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")
    compileOnly("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")
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
    version { taboolib = "6.2.3" }
    env {
        // Bukkit 平台 + 配置模块（读取 config.yml）。
        // 配置模块在 TabooLib 6.2.x 的工件名为 basic-configuration。
        install("platform-bukkit")
        install("basic-configuration")
    }
    // 重定位 taboolib 包，避免与同服其它 TabooLib 插件冲突。
    relocate("taboolib", "${project.group}.taboolib")
    // 重定位第三方库，避免与其它插件携带的 okhttp/okio/kotlinx 版本冲突。
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
