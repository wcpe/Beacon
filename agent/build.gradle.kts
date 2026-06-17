// Beacon agent 根构建脚本：声明各子模块共用的插件版本与编译约定。
plugins {
    // 版本集中在 settings.gradle.kts 的 pluginManagement 声明；此处仅占位、不直接应用。
    kotlin("jvm") apply false
    id("io.izzel.taboolib") apply false
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
