import java.util.jar.JarFile
import org.objectweb.asm.ClassReader
import org.objectweb.asm.ClassVisitor
import org.objectweb.asm.FieldVisitor
import org.objectweb.asm.MethodVisitor
import org.objectweb.asm.Opcodes
import org.objectweb.asm.Type

// apiDump 需要读编译产物的字节码来导出公开签名。用 ASM 解析常量池而非 `Class.forName`
// 加载类：① 不触发类初始化与依赖解析，产物有缺失依赖也能导出；② 全仓零反射约束。
buildscript {
    repositories {
        mavenCentral()
    }
    dependencies {
        classpath("org.ow2.asm:asm:9.11.1")
    }
}

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

// —— 对外 API 兼容性门禁（ADR-0007 的落地检查）——
//
// agent-api 是业务插件**在编译期**依赖的只读契约：插件用某个版本编译后，可能跑在更老的
// agent 运行时上。因此破坏性变更（删方法 / 改签名 / 改返回类型）不是「内部重构」，而是
// 让已发布的插件在最要命的地方炸：插件的类初始化抛 NoClassDefFoundError，agent 根本兜不住。
//
// 这里用**公开 API 签名快照**做门禁，而不引第三方插件（japicmp 之类）：
//   1. `apiDump`   生成当前源码的全部 public/protected 签名到 api/agent-api.api
//   2. `apiCheck`  与已入库的基线逐行比对，任何**删除或修改**都判失败
//   3. 新增签名允许通过（加性演进是本工程的既定约束）
//
// 基线文件必须随改动一起提交：apiCheck 失败时把新基线一起提交，等于显式承认
// 「这次改了对外契约」，评审时看得见。刻意不做「自动更新基线」——那会让门禁形同虚设。

val apiBaselineFile = layout.projectDirectory.file("api/agent-api.api")

/** 生成公开 API 签名：逐个 class 反射 public/protected 成员，输出稳定排序的文本。 */
val apiDump by tasks.registering {
    description = "生成 agent-api 公开 API 签名快照（加性演进的可评审基线）。"
    group = "verification"
    dependsOn(tasks.named("jar"))
    val jarTask = tasks.named<Jar>("jar")
    val outputFile = layout.buildDirectory.file("api/agent-api.api")
    inputs.files(jarTask)
    outputs.file(outputFile)
    doLast {
        val jar = jarTask.get().archiveFile.get().asFile
        val out = outputFile.get().asFile
        out.parentFile.mkdirs()
        val classes = mutableListOf<ApiClass>()
        JarFile(jar).use { jf ->
            for (entry in jf.entries()) {
                val path = entry.name
                // 与旧实现保持一致：只看顶层类（跳过内部类），它们才是对外契约的粒度。
                if (!path.endsWith(".class") || path.contains('$')) continue
                classes += readApiClass(jf.getInputStream(entry).use { it.readBytes() })
            }
        }
        val lines = mutableListOf<String>()
        for (c in classes.sortedBy { it.name }) {
            // 只记录对外可见的类型（package-private 类不是契约）。
            if (!c.isPublic) continue
            lines += "class ${c.name}${if (c.isInterface) " (interface)" else ""}"
            c.ctors.sorted().forEach { lines += "  ctor $it" }
            c.methods.sorted().forEach { lines += "  method $it" }
            c.fields.sorted().forEach { lines += "  field $it" }
        }
        out.writeText(lines.joinToString("\n", postfix = "\n"), Charsets.UTF_8)
    }
}

/** 一个类导出的公开契约。 */
private class ApiClass(
    val name: String,
    val isPublic: Boolean,
    val isInterface: Boolean,
    val ctors: List<String>,
    val methods: List<String>,
    val fields: List<String>,
)

/**
 * 把一个 class 文件的公开/protected 成员读成签名文本。
 *
 * 全程只解析字节码，不 `Class.forName` 加载类——那会触发初始化与依赖解析，
 * 产物若引用了当前类路径没有的类型就会整批导出失败。
 */
private fun readApiClass(bytes: ByteArray): ApiClass {
    var access = 0
    var internalName = ""
    val ctors = mutableListOf<String>()
    val methods = mutableListOf<String>()
    val fields = mutableListOf<String>()

    ClassReader(bytes).accept(
        object : ClassVisitor(Opcodes.ASM9) {
            override fun visit(
                version: Int,
                classAccess: Int,
                name: String,
                signature: String?,
                superName: String?,
                interfaces: Array<out String>?,
            ) {
                access = classAccess
                internalName = name
            }

            override fun visitMethod(
                methodAccess: Int,
                name: String,
                descriptor: String,
                signature: String?,
                exceptions: Array<out String>?,
            ): MethodVisitor? {
                if (!isApiVisible(methodAccess)) return null
                // 静态初始化块不是 API；<init> 才是构造器。
                if (name == "<clinit>") return null
                val type = Type.getMethodType(descriptor)
                val params = type.argumentTypes.joinToString(",") { apiTypeName(it) }
                if (name == "<init>") {
                    ctors += "${internalName.substringAfterLast('/')}($params)"
                } else {
                    // 编译器生成的合成方法 / 桥接方法不是契约（与旧实现一致）。
                    if (methodAccess and (Opcodes.ACC_SYNTHETIC or Opcodes.ACC_BRIDGE) != 0) return null
                    val mods =
                        buildList {
                            if (methodAccess and Opcodes.ACC_STATIC != 0) add("static")
                            if (methodAccess and Opcodes.ACC_ABSTRACT != 0) add("abstract")
                            if (isDefaultMethod(methodAccess, access)) add("default")
                        }.joinToString(" ")
                    val suffix = if (mods.isEmpty()) "" else " [$mods]"
                    methods += "$name($params):${apiTypeName(type.returnType)}$suffix"
                }
                return null
            }

            override fun visitField(
                fieldAccess: Int,
                name: String,
                descriptor: String,
                signature: String?,
                value: Any?,
            ): FieldVisitor? {
                if (!isApiVisible(fieldAccess)) return null
                if (fieldAccess and Opcodes.ACC_SYNTHETIC != 0) return null
                val mods = if (fieldAccess and Opcodes.ACC_STATIC != 0) "static " else ""
                fields += "$name:${apiTypeName(Type.getType(descriptor))} [$mods]"
                return null
            }
        },
        ClassReader.SKIP_CODE or ClassReader.SKIP_DEBUG or ClassReader.SKIP_FRAMES,
    )

    return ApiClass(
        name = internalName.replace('/', '.'),
        isPublic = access and Opcodes.ACC_PUBLIC != 0,
        isInterface = access and Opcodes.ACC_INTERFACE != 0,
        ctors = ctors,
        methods = methods,
        fields = fields,
    )
}

/** public / protected 才是对外契约。 */
private fun isApiVisible(memberAccess: Int): Boolean =
    memberAccess and (Opcodes.ACC_PUBLIC or Opcodes.ACC_PROTECTED) != 0

/** 等价 `java.lang.reflect.Method.isDefault()`：接口里的非抽象、非静态方法。 */
private fun isDefaultMethod(methodAccess: Int, classAccess: Int): Boolean =
    classAccess and Opcodes.ACC_INTERFACE != 0 &&
        methodAccess and (Opcodes.ACC_ABSTRACT or Opcodes.ACC_STATIC) == 0

/**
 * 与 `Class.getName()` 输出保持一致的类型名。
 *
 * 数组必须用描述符形态（`[Lfoo.Bar;` / `[I`）而不是 `foo.Bar[]`——否则基线快照会整体变形。
 */
private fun apiTypeName(type: Type): String =
    if (type.sort == Type.ARRAY) type.descriptor.replace('/', '.') else type.className


/** 与基线比对：删除或修改既有签名即失败；新增允许（加性）。 */
val apiCheck by tasks.registering {
    description = "校验 agent-api 未发生破坏性变更（只允许加性演进）。"
    group = "verification"
    dependsOn(apiDump)
    val dumped = layout.buildDirectory.file("api/agent-api.api")
    val baseline = apiBaselineFile
    inputs.file(dumped)
    val baselineExists = baseline.asFile.exists()
    if (baselineExists) inputs.file(baseline)
    doLast {
        val current = dumped.get().asFile.readLines().filter { it.isNotBlank() }
        if (!baseline.asFile.exists()) {
            throw GradleException(
                "缺少 API 基线 ${baseline.asFile}。首次启用请执行 `:agent-api:apiDump` 后把生成的 " +
                    "build/api/agent-api.api 复制到 api/agent-api.api 并提交。",
            )
        }
        val base = baseline.asFile.readLines().filter { it.isNotBlank() }
        val currentSet = current.toSet()
        val removed = base.filter { it !in currentSet }
        if (removed.isNotEmpty()) {
            throw GradleException(
                buildString {
                    appendLine("agent-api 发生破坏性变更（删除或修改了已发布的签名）：")
                    removed.take(40).forEach { appendLine("  - $it") }
                    if (removed.size > 40) appendLine("  …（共 ${removed.size} 条）")
                    appendLine()
                    appendLine("业务插件编译期依赖本契约，破坏性变更会让已发布插件在旧 agent 上抛 NoClassDefFoundError。")
                    appendLine("若确属计划内破坏性变更：必须走新的 Maven 坐标版本 + 兼容矩阵（见 behavior_fence 协议演进条），")
                    appendLine("并把 ${baseline.asFile} 一并更新后同 PR 提交，让评审看得见。")
                },
            )
        }
        val added = current.filter { it !in base.toSet() }
        if (added.isNotEmpty()) {
            logger.lifecycle("agent-api 新增 ${added.size} 条签名（加性演进，允许）：")
            added.take(20).forEach { logger.lifecycle("  + $it") }
            if (added.size > 20) logger.lifecycle("  …（共 ${added.size} 条）")
        }
    }
}

// 绑定到 agent 的静态检查任务：CI 跑 detekt 时一并校验，不需要额外 workflow 步骤。
tasks.matching { it.name == "check" }.configureEach { dependsOn(apiCheck) }
