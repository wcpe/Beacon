# Beacon 多阶段构建：node 构建前端 dist → go 内嵌编译 → 极小运行镜像。

# —— 阶段一：构建前端 dist ——
FROM node:22-alpine AS web
WORKDIR /web
# 启用 corepack，按 package.json 的 packageManager 字段使用固定版 pnpm
RUN corepack enable
# 先拷依赖清单以利用层缓存
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm run build

# —— 阶段二：Go 内嵌编译 ——
FROM golang:1.26-alpine AS build
# Go 模块代理：默认官方代理，受限网络可经构建参数注入镜像（如 https://goproxy.cn,direct）
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
# 先拉依赖以利用层缓存
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 注入前端构建产物供 go:embed 内嵌（覆盖占位 .gitkeep）
COPY --from=web /web/dist ./web/dist
# 静态链接、去符号表，产出极小二进制：控制面 beacon 与 launcher 监督进程 beacon-launcher（FR-96/ADR-0045）
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/beacon ./cmd/beacon \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/beacon-launcher ./cmd/beacon-launcher

# —— 阶段三：极小运行镜像 ——
FROM alpine:3.20
# 创建非 root 运行账户
RUN addgroup -S beacon && adduser -S -G beacon beacon
# launcher 按 os.Executable() 同目录定位 beacon，故两二进制须同放 /usr/local/bin
COPY --from=build /out/beacon /usr/local/bin/beacon
COPY --from=build /out/beacon-launcher /usr/local/bin/beacon-launcher
USER beacon
# API 与管理台 UI 同端口
EXPOSE 8848
# 入口走 launcher 监督进程（两形态统一入口，容器内监督主进程崩溃自动重启）。
# 注意：容器内自更新换二进制临时有效，但镜像不可变——容器重建会丢更新，生产升级以重拉镜像为准（见 docs/OPERATIONS.md）。
ENTRYPOINT ["/usr/local/bin/beacon-launcher"]
