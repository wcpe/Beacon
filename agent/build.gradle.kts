// Beacon agent 根构建脚本：声明各子模块共用的插件版本与编译约定。
// M0 仅搭可编译骨架，真正的接入逻辑在 M5 实现。
plugins {
    // 在根工程声明插件版本，子模块按需 apply（此处不直接应用）。
    kotlin("jvm") version "1.9.22" apply false
    id("io.izzel.taboolib") version "2.0.37" apply false
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
