// agent-api：业务插件 compileOnly 依赖的只读契约。纯 Java 8，无 Kotlin、无任何第三方依赖。
// 公开签名只用 java.util.* / java.util.Optional / java.util.function.*，不引用其它模块的内部类型。
plugins {
    `java-library`
}

// Java 8 源码与目标字节码（与全工程一致；根 build.gradle.kts 的 JavaPlugin 配置亦会兜底）。
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
}
