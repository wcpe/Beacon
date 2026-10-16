// Package version 暴露控制面版本号，构建时由 -ldflags -X 注入（唯一源为仓库根 VERSION，见 ADR-0007）。
package version

// Version 是控制面版本号。
// 默认 "dev"（直接 go run / 未经打包构建时）；发布 / 打包构建经
// go build -ldflags "-X github.com/wcpe/Beacon/apps/server/internal/version.Version=..." 注入真实版本。
//
// RC 候选构建注入带候选后缀的版本（如 "1.5.0-rc.2"），使 --version、启动日志、状态页与更新页
// 一眼可辨是哪个候选（ADR-0092）。GA 复用 RC 同字节资产、不重建（ADR-0074 第 3 条），
// 因此 GA 安装同样报告该串——这是原样晋级的必然结果，不是版本号错误。
//
// 该字段是更新判新的输入，internal/update 的 parseRuntimeVersion 已支持候选版本比较；
// 而 GA tag 校验（parseGATag）仍严格拒绝带候选后缀的 tag，故候选不会进入自动更新候选集。
var Version = "dev"
