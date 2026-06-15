# ADR-0005：agent 的 HTTP 客户端与 JSON 序列化走抽象层

**状态**：已接受

## 背景

agent（Kotlin/TabooLib）需要一个 HTTP 客户端与 JSON 序列化库与控制面通信。默认选 OkHttp + kotlinx.serialization，但担心后续与 TabooLib/其他插件的依赖冲突，希望能随时替换。

## 决策

`agent-core` **只依赖抽象接口**，不直接依赖具体库：

```kotlin
interface HttpTransport { fun execute(req: HttpRequest): HttpResponse }   // 可换 OkHttp / JDK HttpClient / ...
interface JsonCodec { fun <T> encode(v: T): String; fun <T> decode(s: String, t: KType): T }  // 可换 kotlinx / Gson / Jackson
class BeaconApiClient(transport: HttpTransport, codec: JsonCodec)         // 收口 register/heartbeat/pollEffective/report/discover
```

默认适配器 `OkHttpTransport`、`KotlinxJsonCodec` 是**唯一**碰具体库的类。bootstrap 时装配默认实现。

## 理由

- 把第三方库限制在适配器边界内，core 与业务逻辑零依赖具体库。
- 后续若与 TabooLib 重定位（relocate）或其他插件冲突，替换 = 换注入的适配器实现，core 不动。
- 符合依赖倒置 / 端口适配器，可测性更好（测试可注入假 transport）。

## 后果

- 多一层接口与适配器（极薄），换来可替换性与可测性，值得。

## 备选方案

- **直接在 core 用 OkHttp + kotlinx**：少一层，但库与业务焊死，冲突时改动面大。被否。
