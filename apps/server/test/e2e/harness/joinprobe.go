//go:build e2e

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件承载 FR-276「零配置接入（/beacon join）」真机用例所需的运行目录操作。
//
// ## 为什么必须单独抽出来
//
// 既有 e2e 全部走「注入环境变量」的接入方式（[AgentGradleEnv]），运行目录里
// 只要有 identity.yml 就能拿到身份，config.yml 填不填都无所谓。join 用例恰恰相反：
//   - **不能**注入接入目标与令牌（否则测的是注入路径，不是 join）；
//   - 运行目录必须是**干净出厂态**（无凭据、无身份），否则 join 会因为「已绑定」而被拒绝，
//     或者直接沿用上轮的凭据（假通过）。
//
// 这些前置一旦散落在测试函数里，任何一个用例漏掉一步都会得到「看起来通过」的结果，
// 故收在一处并各自守卫路径，让「干净」这件事只有一个定义。

// AgentDataDir 返回 Bukkit 侧 BeaconAgent 的数据目录（运行目录下的 plugins/BeaconAgent）。
func AgentDataDir(runDir string) string {
	return filepath.Join(runDir, "plugins", "BeaconAgent")
}

// ProbeDataDir 返回 BeaconE2E 验收插件的数据目录（运行目录下的 plugins/BeaconE2E）。
func ProbeDataDir(runDir string) string {
	return filepath.Join(runDir, "plugins", "BeaconE2E")
}

// AgentCredentialsPath 返回 join 写入的凭据文件路径。
//
// 与 identity.yml 分开是本功能的核心约定：凭据（含令牌）绝不写进 config.yml，
// 也不与身份文件混存（见 AgentCredentialsStore 的类注释）。
func AgentCredentialsPath(runDir string) string {
	return filepath.Join(AgentDataDir(runDir), "agent-credentials.json")
}

// AgentIdentityPath 返回 agent 自管身份文件路径。
func AgentIdentityPath(runDir string) string {
	return filepath.Join(AgentDataDir(runDir), "identity.yml")
}

// AgentPushedConfigPath 返回控制面下发配置的本地缓存路径。
func AgentPushedConfigPath(runDir string) string {
	return filepath.Join(AgentDataDir(runDir), "pushed-config.json")
}

// JoinRequestPath 返回 join 代执行探针读取的请求文件路径。
func JoinRequestPath(runDir string) string {
	return filepath.Join(ProbeDataDir(runDir), "e2e-join-request.txt")
}

// JoinObservationPath 返回 join 探针的观测文件路径。
func JoinObservationPath(runDir string) string {
	return filepath.Join(ProbeDataDir(runDir), "e2e-join.log")
}

// PlaceholderAgentConfigYAML 是**带模板占位符**的出厂 config.yml 形态。
//
// 这串内容正是历史 bug 的触发器：占位符非空、又不指向真实地址，旧实现会把它们
// 当成「运维已显式配置」，于是 `/beacon join` 被静默忽略、占位符还会被写进凭据文件
// （之后即使清空 config.yml 也救不回来）。用例据此断言两件事：
//   - 留着占位符时 join **仍然成功**（占位符等价于未配置）；
//   - 凭据里**不出现**占位符字面量。
const PlaceholderAgentConfigYAML = `# BeaconAgent E2E：故意保留出厂模板占位符，验证 join 不被占位符挡住。
beacon:
  endpoints:
    - "<CONTROL_PLANE_URL>"
  bootstrap-token: "<NAMESPACE_ACCESS_TOKEN>"
`

// PlaceholderEndpointLiteral 是 config.yml 占位符地址字面量（断言凭据不得包含它）。
const PlaceholderEndpointLiteral = "<CONTROL_PLANE_URL>"

// PlaceholderTokenLiteral 是 config.yml 占位符令牌字面量（断言凭据不得包含它）。
const PlaceholderTokenLiteral = "<NAMESPACE_ACCESS_TOKEN>"

// ResetAgentDataDir 把 agent 数据目录恢复成「全新出厂态」：整目录删除后重建。
//
// ## 为什么整目录删而不是逐个删文件
//
// agent 的数据目录里除了 identity.yml / 凭据，还有多份快照与清单
// （identity-binding.snapshot.json、file-tree.applied.json、effective-config.snapshot.json…）。
// 逐个删极易在功能演进后漏掉新文件，而漏掉的那份会让 agent 以为「自己已接入过」。
// 该目录是构建产物（mc-testkit 运行目录内），删了会由本轮启动重新生成。
//
// @param runDir mc-testkit 运行目录（[BackendRunDir] 的返回值）
func ResetAgentDataDir(t testing.TB, runDir string) {
	t.Helper()
	dir := AgentDataDir(runDir)
	// 路径守卫：只允许删 plugins/BeaconAgent，避免调用方传错目录把别的东西清掉。
	if filepath.Base(filepath.Dir(dir)) != "plugins" || filepath.Base(dir) != "BeaconAgent" {
		t.Fatalf("拒绝清理非 agent 数据目录：%s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("清理 agent 数据目录失败（%s）：%v", dir, err)
	}
}

// WriteAgentBootstrapConfig 写入 agent 的本地引导配置（config.yml）。
//
// 用于构造「config.yml 里留着模板占位符」的出厂形态。必须在 Paper 启动**之前**写：
// agent 只在文件缺失时才生成默认配置，已存在的 config.yml 会被原样保留。
func WriteAgentBootstrapConfig(t testing.TB, runDir, content string) {
	t.Helper()
	dir := AgentDataDir(runDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建 agent 数据目录失败（%s）：%v", dir, err)
	}
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 agent 引导配置失败（%s）：%v", path, err)
	}
}

// WriteJoinRequest 投递一条 join 请求给代执行探针（`endpoint=...` / `token=...` 两行）。
//
// 令牌只经这个文件传递、且探针派发后不会把它写进任何日志；文件本身位于运行目录内，
// 由本轮构建产物承载，不进版本库。
func WriteJoinRequest(t testing.TB, runDir, endpoint, token string) {
	t.Helper()
	path := JoinRequestPath(runDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建探针数据目录失败：%v", err)
	}
	body := fmt.Sprintf("endpoint=%s\ntoken=%s\n", endpoint, token)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("投递 join 请求失败（%s）：%v", path, err)
	}
}

// RemoveStaleJoinRequest 删除上轮残留的 join 请求文件。
//
// **必须删**：探针只要看到请求就会派发，上轮的陈旧请求会在本轮 agent 尚未注册命令时
// 被反复重试，或用旧令牌把本轮 join 顶掉，表现为「凭据里的 token 对不上」。
func RemoveStaleJoinRequest(t testing.TB, runDir string) {
	t.Helper()
	path := JoinRequestPath(runDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("清理残留 join 请求文件失败（%s）：%v", path, err)
	}
}

// AgentCredentials 是凭据文件的解析形态（只取本用例关心的接入三要素）。
type AgentCredentials struct {
	FormatVersion int    `json:"formatVersion"`
	IdentityID    string `json:"identityId"`
	Endpoint      string `json:"endpoint"`
	Token         string `json:"token"`
}

// ReadAgentCredentials 读取并解析 agent 凭据文件（不存在时返回错误）。
//
// 解析而不是纯文本匹配：需要区分「凭据里的 endpoint 是空」与「凭据里的 endpoint 是占位符」，
// 这两者对应完全不同的缺陷（前者是还没 join，后者是占位符被当成真实配置）。
func ReadAgentCredentials(runDir string) (AgentCredentials, error) {
	path := AgentCredentialsPath(runDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return AgentCredentials{}, fmt.Errorf("读取 agent 凭据失败（%s）：%w", path, err)
	}
	var out AgentCredentials
	if err := json.Unmarshal(raw, &out); err != nil {
		return AgentCredentials{}, fmt.Errorf("解析 agent 凭据失败（%s）：%w；原文=%s", path, err, string(raw))
	}
	return out, nil
}

// AssertCredentialsUsable 断言凭据里 endpoint/token 均为给定值，且不含模板占位符。
//
// 这里刻意同时做**解析级**与**字面量级**检查：
//   - 解析级：字段值必须逐字等于本次 join 传入的地址与令牌（证明是 join 写的）；
//   - 字面量级：整份凭据不得出现占位符文本——占位符回归的形态正是
//     「字段有值、值却是 <CONTROL_PLANE_URL>」，只断言「字段非空」会放过它。
func AssertCredentialsUsable(runDir, wantEndpoint, wantToken string) error {
	cred, err := ReadAgentCredentials(runDir)
	if err != nil {
		return err
	}
	if cred.Endpoint != wantEndpoint {
		return fmt.Errorf("凭据 endpoint 应为 %q，实际 %q", wantEndpoint, cred.Endpoint)
	}
	if cred.Token != wantToken {
		return fmt.Errorf("凭据 token 应是本次 join 传入的令牌，实际 %q", cred.Token)
	}
	path := AgentCredentialsPath(runDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("复读 agent 凭据失败（%s）：%w", path, err)
	}
	for _, placeholder := range []string{PlaceholderEndpointLiteral, PlaceholderTokenLiteral} {
		if strings.Contains(string(raw), placeholder) {
			return fmt.Errorf("凭据里出现了 config.yml 的模板占位符 %q（回归：占位符被当成真实配置写入）：%s", placeholder, raw)
		}
	}
	return nil
}

// AssertCredentialsNotJoined 断言「尚未 join」的形态：凭据可以存在（agent 启动即生成身份），
// 但**不得**具备可连接条件，也不得含占位符。
//
// 为什么不能断言「凭据文件不存在」：agent 在 enable 阶段就会把新身份落盘到凭据
// （见 AgentCredentialsStore.resolveIdentityId 的「新生成并落盘」分支），
// 用的是 config.yml 里**过滤掉占位符后**的种子（此处为空）。真正要证明的是
// 「接入信息还没到」——即 endpoint/token 都为空。若接入信息从环境变量漏进来，
// 这里就会拿到非空值而失败：那正是「测的是注入路径而不是 join」的形态。
func AssertCredentialsNotJoined(runDir string) error {
	cred, err := ReadAgentCredentials(runDir)
	if err != nil {
		return err
	}
	if cred.Endpoint != "" || cred.Token != "" {
		return fmt.Errorf(
			"未执行 join，凭据里却已有接入信息（endpoint=%q token=%q）：接入信息必然来自注入而非 join",
			cred.Endpoint, cred.Token,
		)
	}
	if cred.IdentityID == "" {
		return fmt.Errorf("未执行 join 时凭据应已带本机身份（agent 启动即生成），实际为空：%+v", cred)
	}
	return nil
}
