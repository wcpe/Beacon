// Package beacon 是模块根包，仅用于内嵌前端构建产物。
package beacon

import "embed"

// WebDist 内嵌第二版前端构建产物（apps/web/dist）。生产构建时由 `make web` 产出，
// 本地未构建前端时仅含占位 .gitkeep，不影响后端编译。
//
//go:embed all:apps/web/dist
var WebDist embed.FS

// ConfigExampleYAML 内嵌控制面配置模板，供首次启动释放为 config.yml（FR-25）。
//
//go:embed config.example.yml
var ConfigExampleYAML []byte
