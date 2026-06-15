# ADR-0002：Go 后端 + 内嵌 React 单二进制技术栈

**状态**：已接受

## 背景

控制面是独立后端服务，需要 API + 管理台 UI，单节点部署、运维简单。MC 侧 agent 是 JVM（Kotlin/TabooLib）。

## 决策

控制面用 **Go**（HTTP 框架 chi + GORM）；管理台用 **React(Vite+TS)**，构建产物经 `//go:embed` 内嵌进 Go 二进制，**单二进制同端口**同时供 API 与 UI；**docker-compose** 部署（beacon + mysql）。

## 理由

- Go 单静态二进制、镜像极小、内存低、goroutine 扛大量 agent 长连接，启动快、运维省。
- `go:embed` 把 React `dist` 焊进二进制，运维只发一个文件，无需单独部署前端。
- 同端口同源访问，无 CORS。
- chi 贴标准库 `net/http`，长轮询（手动 `select` + `ctx.Done()` 感知断连 + `Shutdown` 优雅等待）与标准库语义一致，不被框架抽象挡路。

## 后果

- **控制面（Go）与 agent（JVM）的集成缝是语言中立的线协议**（REST/JSON），不再是共享 JVM 模块；契约用本仓 `docs/API.md` 描述，两侧各自实现。
- 这反而让控制面彻底独立于 JVM 生态（也不依赖 CoreLib）。

## 备选方案

- **JVM（Spring Boot）后端**：与 agent 同生态、可共享模块，但重、镜像大、启动慢，且把控制面绑死在 JVM。被否。
- **前后端分离部署**：多一个部署单元与 CORS/反代复杂度，单节点 MVP 不值。被否。
