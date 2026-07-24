# Beacon 多阶段构建：node 构建第二版前端 dist → go 内嵌编译 → 极小运行镜像。

# —— 阶段一：构建前端 dist ——
FROM node:22-alpine AS web
WORKDIR /workspace
# 前端包管理器镜像可由构建参数覆盖（国内受限网络可用 npmmirror）
ARG NPM_CONFIG_REGISTRY=https://registry.npmjs.org
ARG COREPACK_NPM_REGISTRY=https://registry.npmjs.org
ENV NPM_CONFIG_REGISTRY=${NPM_CONFIG_REGISTRY}
ENV COREPACK_NPM_REGISTRY=${COREPACK_NPM_REGISTRY}
# 启用 corepack，按 package.json 的 packageManager 字段使用固定版 pnpm
RUN corepack enable
# 先拷依赖清单以利用层缓存
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/web/package.json ./apps/web/package.json
COPY packages/ui/package.json ./packages/ui/package.json
COPY packages/contracts/package.json ./packages/contracts/package.json
COPY packages/devmock/package.json ./packages/devmock/package.json
COPY packages/eslint-config/package.json ./packages/eslint-config/package.json
COPY packages/typescript-config/package.json ./packages/typescript-config/package.json
RUN pnpm install --frozen-lockfile
COPY apps/web ./apps/web
COPY packages ./packages
RUN pnpm --filter @beacon/web build

# —— 阶段二：Go 内嵌编译（纯 Go sqlite，无需 CGO）——
FROM golang:1.26-alpine AS build
# Go 模块代理：默认官方代理，受限网络可经构建参数注入镜像（如 https://goproxy.cn,direct）
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
# 先拉依赖以利用层缓存
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 注入第二版前端构建产物供 go:embed 内嵌（覆盖占位 .gitkeep）
COPY --from=web /workspace/apps/web/dist ./apps/web/dist
# 纯 Go 构建：无 CGO，sqlite 走 modernc/glebarez
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/beacon ./apps/server/cmd/beacon

# —— 阶段三：极小运行镜像 ——
FROM alpine:3.20
# 国内 Alpine 源 + 非 root 运行账户 + 可写 /data（config.yml 与 sqlite）
RUN sed -i 's#https\?://dl-cdn.alpinelinux.org/alpine#https://mirrors.aliyun.com/alpine#g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates \
    && addgroup -S beacon \
    && adduser -S -G beacon beacon \
    && mkdir -p /data \
    && chown beacon:beacon /data
COPY --from=build /out/beacon /usr/local/bin/beacon
WORKDIR /data
USER beacon
# API 与管理台 UI 同端口
EXPOSE 8848
# 入口为单进程 beacon；进程崩溃的自动重启靠 compose 的 restart 策略。
# 注意：容器内自更新换二进制临时有效，但镜像不可变——容器重建会丢更新，生产升级以重拉镜像为准（见 docs/OPERATIONS.md）。
ENTRYPOINT ["/usr/local/bin/beacon"]
