# Beacon 多阶段构建：node 构建前端 dist → go 内嵌编译 → 极小运行镜像。

# —— 阶段一：构建前端 dist ——
FROM node:22-alpine AS web
WORKDIR /web
# 先拷依赖清单以利用层缓存
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

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
# 静态链接、去符号表，产出极小二进制
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/beacon ./cmd/beacon

# —— 阶段三：极小运行镜像 ——
FROM alpine:3.20
# 创建非 root 运行账户
RUN addgroup -S beacon && adduser -S -G beacon beacon
COPY --from=build /out/beacon /usr/local/bin/beacon
USER beacon
# API 与管理台 UI 同端口
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/beacon"]
