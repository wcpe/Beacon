// agent-kit：下游业务插件可选依赖的便捷接入层。纯 Java 8，零第三方依赖，只依赖 agent-api。
// 封装下游样板：isAvailable 回退判据（绝不看 connected，防 split-brain）、读合并配置 / 查发现的便捷方法、
// 配置变更订阅桥（注册即重放 + agent 由不可用转可用后补注册重放）。不碰线程调度（切线程留给下游）。
plugins {
    `java-library`
    // 发布到 Maven 仓库（默认 mavenLocal，可选远程经 property/env 注入，见根 build.gradle.kts 的发布约定）。
    `maven-publish`
}

// Java 8 源码与目标字节码（与全工程一致）。
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
    // 一并产出 sources jar，便于下游 IDE 跳转。
    withSourcesJar()
}

dependencies {
    // 唯一依赖：只读契约 agent-api（纯 Java，kit 可引用其类型）。
    api(project(":agent-api"))

    // 单元测试：纯逻辑（回退判据 / 订阅桥），用假门面驱动，不连任何外部依赖。
    testImplementation("org.junit.jupiter:junit-jupiter:5.10.2")
}

// 启用 JUnit 平台。
tasks.test {
    useJUnitPlatform()
}

// 发布坐标：group=top.wcpe.beacon、artifact=beacon-agent-kit、version 跟随根 VERSION（ADR-0007）。
// 对 agent-api 的项目依赖在 POM 里自动映射为其发布坐标 top.wcpe.beacon:beacon-agent-api
// （因 agent-api 已声明同名 MavenPublication，Gradle 据此改写依赖坐标）。
publishing {
    publications {
        create<MavenPublication>("maven") {
            groupId = "top.wcpe.beacon"
            artifactId = "beacon-agent-kit"
            from(components["java"])
        }
    }
}
