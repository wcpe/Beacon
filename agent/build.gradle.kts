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
}
