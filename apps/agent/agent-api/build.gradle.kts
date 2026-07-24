// agent-api：业务插件 compileOnly 依赖的只读契约。纯 Java 8，无 Kotlin、无任何第三方依赖。
// 公开签名只用 java.util.* / java.util.Optional / java.util.function.*，不引用其它模块的内部类型。
plugins {
    `java-library`
    // 发布到 Maven 仓库（默认 mavenLocal，可选远程经 property/env 注入，见根 build.gradle.kts 的发布约定）。
    `maven-publish`
}

// Java 8 源码与目标字节码（与全工程一致；根 build.gradle.kts 的 JavaPlugin 配置亦会兜底）。
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
    // Central Portal 要求同时提供源码与文档产物。
    withSourcesJar()
    withJavadocJar()
}

// 发布坐标：group=top.wcpe.beacon、artifact=beacon-agent-api、version 跟随根 VERSION（ADR-0007）。
// groupId 比构建期 Gradle group（top.wcpe.beacon.agent）更短，作为对外 SDK 坐标。
publishing {
    publications {
        create<MavenPublication>("maven") {
            groupId = "top.wcpe.beacon"
            artifactId = "beacon-agent-api"
            from(components["java"])
            pom {
                name.set("Beacon Agent API")
                description.set("Beacon 业务插件只读接入契约")
                url.set("https://github.com/wcpe/Beacon")
                licenses {
                    license {
                        name.set("MIT License")
                        url.set("https://opensource.org/license/mit")
                        distribution.set("repo")
                    }
                }
                developers {
                    developer {
                        id.set("wcpe")
                        name.set("wcpe")
                    }
                }
                scm {
                    connection.set("scm:git:https://github.com/wcpe/Beacon.git")
                    developerConnection.set("scm:git:ssh://git@github.com/wcpe/Beacon.git")
                    url.set("https://github.com/wcpe/Beacon")
                }
            }
        }
    }
}
