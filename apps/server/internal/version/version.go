// Package version 暴露控制面版本号，构建时由 -ldflags -X 注入（唯一源为仓库根 VERSION，见 ADR-0007）。
package version

import "strings"

// Version 是控制面正式版本号 X.Y.Z。
// 默认 "dev"（直接 go run / 未经打包构建时）；发布 / 打包构建经
// go build -ldflags "-X github.com/wcpe/Beacon/apps/server/internal/version.Version=$(cat VERSION)" 注入真实版本。
//
// 该字段必须保持三段纯数字：在线更新判新（internal/update 的 parseSemver）与换版对账都直接消费它，
// 附加任何后缀都会让更新检查与执行失效。需要可辨识的候选标记请用 Display()。
var Version = "dev"

// Candidate 是候选标记（形如 "rc.2"），发布构建注入；GA 与本地构建留空。
// 与 Version 分开存放的原因见 Version 注释：正式版本号必须保持可解析。
var Candidate = ""

// Commit 是构建来源的短提交号（8 位），发布构建注入；无 git 环境时留空。
var Commit = ""

// Display 返回供人辨识的版本串：正式版本 + 候选标记（如有）+ 构建提交（如有）。
// 例："1.5.0-rc.2 (114cf533)"。用于 --version、启动日志与状态页展示。
//
// 同一正式版本的不同候选（rc.2 相对 rc.1）在此必然可辨，无需比对哈希或文件大小。
// GA 复用最终 RC 的同字节资产（ADR-0074 第 3 条，晋级不重建），因此 GA 会如实显示产出该字节的候选标记。
func Display() string {
	var builder strings.Builder
	builder.WriteString(Version)
	if Candidate != "" {
		builder.WriteString("-")
		builder.WriteString(Candidate)
	}
	if Commit != "" {
		builder.WriteString(" (")
		builder.WriteString(Commit)
		builder.WriteString(")")
	}
	return builder.String()
}
