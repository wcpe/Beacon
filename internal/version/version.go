// Package version 暴露控制面版本号，构建时由 -ldflags -X 注入（唯一源为仓库根 VERSION，见 ADR-0007）。
package version

// Version 是控制面版本号。
// 默认 "dev"（直接 go run / 未经打包构建时）；发布 / 打包构建经
// go build -ldflags "-X github.com/wcpe/Beacon/internal/version.Version=$(cat VERSION)" 注入真实版本。
var Version = "dev"
