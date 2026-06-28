// Beacon agent 根构建脚本：声明各子模块共用的插件版本与编译约定。
plugins {
    // 版本集中在 settings.gradle.kts 的 pluginManagement 声明；此处仅占位、不直接应用。
    kotlin("jvm") apply false
    id("io.izzel.taboolib") apply false
    // ktlint / detekt 静态检查插件：根仅占位，真正按模块在 subprojects 中按是否含 Kotlin 插件择优应用。
    id("org.jlleitschuh.gradle.ktlint") apply false
    id("io.gitlab.arturbosch.detekt") apply false
}

// 从仓库根 VERSION 文件读取版本号并注入所有模块（ADR-0007：根 VERSION 为唯一版本来源、三组件版本恒一致）。
// agent 为独立 Gradle 构建（根在 agent/），VERSION 位于其上一级；worktree 下同样成立。
val beaconVersion: String = rootProject.projectDir.parentFile.resolve("VERSION").readText().trim()

allprojects {
    version = beaconVersion
}

// 所有子模块统一的仓库与编译约定。
subprojects {
    repositories {
        mavenCentral()
        // TabooLib 官方发布仓库，解析 taboolib 各模块工件。
        maven("https://repo.tabooproject.org/repository/releases")
    }

    // Java 编译统一 UTF-8 编码。
    tasks.withType<JavaCompile>().configureEach {
        options.encoding = "UTF-8"
    }

    // 目标字节码 Java 8（TabooLib 惯例），确保旧版 MC 服务端可加载。
    tasks.withType<org.jetbrains.kotlin.gradle.tasks.KotlinCompile>().configureEach {
        kotlinOptions {
            jvmTarget = "1.8"
        }
    }
    plugins.withType<JavaPlugin> {
        extensions.configure<JavaPluginExtension> {
            sourceCompatibility = JavaVersion.VERSION_1_8
            targetCompatibility = JavaVersion.VERSION_1_8
        }
    }

    // 静态检查：仅对含 Kotlin 插件的模块应用 ktlint + detekt（纯 Java 的 agent-api/agent-kit 不需要）。
    plugins.withType<org.jetbrains.kotlin.gradle.plugin.KotlinPluginWrapper> {
        apply(plugin = "org.jlleitschuh.gradle.ktlint")
        apply(plugin = "io.gitlab.arturbosch.detekt")

        // ktlint：固定 ktlint 引擎版本、关闭对生成代码的检查（build 目录默认已排除）。
        extensions.configure<org.jlleitschuh.gradle.ktlint.KtlintExtension> {
            // 固定 ktlint 引擎版本，保证各机器规则一致。
            version.set("1.2.1")
        }

        // detekt：统一配置文件 + 共享 baseline，目标字节码与工程一致。
        extensions.configure<io.gitlab.arturbosch.detekt.extensions.DetektExtension> {
            // 集中的规则配置文件（仓库内唯一真源）。
            config.setFrom(rootProject.file("config/detekt/detekt.yml"))
            // 以配置文件为准而非内置默认全开，避免与本工程惯例冲突。
            buildUponDefaultConfig = true
            // 共享 baseline：把「真修需冒险大重构正常工作核心类/长方法」的存量 finding 挂起（详见 detekt.yml 顶部说明）。
            // baseline 仅豁免现有这批，规则对新代码仍生效——签名含文件/类名，跨模块全局唯一，故各模块共用一份。
            baseline = rootProject.file("config/detekt/baseline.xml")
            // 不忽略失败：未被 baseline 豁免的新 finding 仍让构建失败。
            ignoreFailures = false
        }
        // detekt 任务统一目标 JVM 字节码版本（与编译目标一致）。
        tasks.withType<io.gitlab.arturbosch.detekt.Detekt>().configureEach {
            jvmTarget = "1.8"
        }
        tasks.withType<io.gitlab.arturbosch.detekt.DetektCreateBaselineTask>().configureEach {
            jvmTarget = "1.8"
        }
    }

    // 发布仓库统一约定（FR-16 SDK 接入包）：默认只发 mavenLocal；远程仓库可选，
    // URL/凭据走 gradle property 或环境变量注入（不硬编码、不入库），缺省即只 mavenLocal。
    // 不发到 repo.tabooproject.org（那是 TabooLib 的、无写权限）。
    plugins.withType<MavenPublishPlugin> {
        extensions.configure<PublishingExtension> {
            repositories {
                // 默认目标：本机 ~/.m2，零配置可用。
                mavenLocal()
                // 可选远程：仅当显式提供 beaconPublishUrl（property）或 BEACON_PUBLISH_URL（env）时启用。
                val remoteUrl = (project.findProperty("beaconPublishUrl") as String?)
                    ?: System.getenv("BEACON_PUBLISH_URL")
                if (!remoteUrl.isNullOrBlank()) {
                    maven {
                        name = "beaconRemote"
                        url = uri(remoteUrl)
                        // 凭据同样可选，走 property 或 env，缺省则匿名（适配无鉴权内网仓库）。
                        val user = (project.findProperty("beaconPublishUsername") as String?)
                            ?: System.getenv("BEACON_PUBLISH_USERNAME")
                        val pass = (project.findProperty("beaconPublishPassword") as String?)
                            ?: System.getenv("BEACON_PUBLISH_PASSWORD")
                        if (!user.isNullOrBlank() && !pass.isNullOrBlank()) {
                            credentials {
                                username = user
                                password = pass
                            }
                        }
                    }
                }
            }
        }
    }
}
