// 本文件**刻意不带 `e2e` 构建标签**，以便普通 `go test ./...`（PR 门禁）也能执行：
// 它守卫的是「agent E2E 模板里的 TabooLib 运行期仓库地址」这一静态配置约定，
// 不需要真机环境。若加上 e2e 标签，该约定破坏时只有在每日/手动的 E2E 流水线才会暴露，
// 而那正是历史上连续 17 天无人察觉的失败形态。
package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// e2eRepoHost 是 wcpe 定制制品的唯一托管仓库。
// 本插件消费的 TabooLib 与 reflex 都是 `*-wcpe.N` 定制版本，只发布在此私仓。
const e2eRepoHost = "repo.wcpe.top"

// upstreamTabooRepoHost 是 TabooLib 上游官方仓库。它对 wcpe 定制坐标一律返回 404，
// 故运行期仓库绝不可指向它。
const upstreamTabooRepoHost = "repo.tabooproject.org"

// e2eTemplateNodes 是需要覆盖的节点模板（后端 Paper 与代理 BungeeCord 各一份）。
var e2eTemplateNodes = []string{"backend", "proxy"}

// e2eRepoKeys 是 TabooLib PrimitiveLoader 在运行期读取的仓库键（见插件内
// META-INF/taboolib/env.properties 与 PrimitiveSettings）。
var e2eRepoKeys = []string{"repo-taboolib", "repo-reflex"}

// TestE2ETemplatesPointToWCPERepo 守住 E2E 模板的运行期仓库地址。
//
// 背景（2026-10 真实故障）：模板曾把 repo-taboolib / repo-reflex 指向
// https://repo.tabooproject.org/repository/releases，而 wcpe 定制制品（如
// reflex-1.2.5-wcpe.1、common-env-6.3.0-wcpe.3）只存在于 repo.wcpe.top。
// 结果：真机 E2E 中 agent 插件 PrimitiveLoader 全部下载 404 → 插件初始化中断 →
// agent 从不写 identity.yml → 所有依赖真实 agent 的 E2E 在等待 12 分钟后超时。
//
// 该回归属「配置错位」，普通编译与单测都无法发现（模板是构建期复制的资源），
// 因此用本测试把它钉住。
func TestE2ETemplatesPointToWCPERepo(t *testing.T) {
	// 本测试文件位于 apps/server/test/e2e/harness/，向上五级即仓库根
	// （harness → e2e → test → server → apps → 根），模板位于
	// apps/agent/agent-e2e/src/e2e-templates/<node>/env.properties。
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("解析仓库根目录失败：%v", err)
	}

	for _, node := range e2eTemplateNodes {
		t.Run(node, func(t *testing.T) {
			path := filepath.Join(
				repoRoot, "apps", "agent", "agent-e2e", "src", "e2e-templates", node, "env.properties",
			)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读取 E2E 模板失败（%s）：%v", path, err)
			}
			values := parseProperties(string(content))

			for _, key := range e2eRepoKeys {
				value, ok := values[key]
				if !ok {
					t.Fatalf("%s 缺少键 %s：运行期将回落到插件内置默认仓库（tabooproject.org），"+
						"wcpe 定制制品会 404 导致 agent 无法启动", path, key)
				}
				if strings.Contains(value, upstreamTabooRepoHost) {
					t.Errorf("%s 的 %s 指向上游仓库 %s（值 %q）：wcpe 定制制品在该仓库不存在，"+
						"会导致 agent 插件运行期下载失败", path, key, upstreamTabooRepoHost, value)
				}
				if !strings.Contains(value, e2eRepoHost) {
					t.Errorf("%s 的 %s 应指向 %s（实际 %q）：定制制品仅发布在该私仓",
						path, key, e2eRepoHost, value)
				}
			}
		})
	}
}

// parseProperties 解析 Java properties 风格的 `键=值` 文本。
// 只需支持本场景：忽略空行与 `#`/`!` 开头的注释行，按首个 `=` 切分并去除首尾空白。
func parseProperties(content string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}
