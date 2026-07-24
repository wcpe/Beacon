package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
)

// parseAs 解析指定格式内容，失败即 Fatal。
func parseAs(t *testing.T, format, content string) map[string]any {
	t.Helper()
	parsed, err := merge.Parse(format, content)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	m, ok := parsed.(map[string]any)
	if !ok {
		t.Fatalf("解析结果应为 map，实际 %T", parsed)
	}
	return m
}

func TestMaskSensitiveContent_嵌套路径(t *testing.T) {
	parsed := parseAs(t, merge.FormatYAML, "database:\n  host: db.local\n  password: s3cr3t\nname: app")
	masked := maskSensitiveContent(parsed, []string{"database.password"}).(map[string]any)
	db := masked["database"].(map[string]any)
	if db["password"] != ConfigMaskedPlaceholder {
		t.Fatalf("嵌套敏感值未脱敏: %v", db["password"])
	}
	if db["host"] != "db.local" || masked["name"] != "app" {
		t.Fatal("非敏感键不应被改动")
	}
	// 原结构必须保持明文（脱敏在深拷贝上进行）
	if parsed["database"].(map[string]any)["password"] != "s3cr3t" {
		t.Fatal("脱敏污染了原始明文结构")
	}
}

func TestMaskSensitiveContent_含点扁平键(t *testing.T) {
	// yaml 中 database.password 作为扁平键（devmock 种子即此形态）
	parsed := parseAs(t, merge.FormatYAML, "database.password: prod-secret\nstart-balance: 100")
	masked := maskSensitiveContent(parsed, []string{"database.password"}).(map[string]any)
	if masked["database.password"] != ConfigMaskedPlaceholder {
		t.Fatalf("扁平敏感键未脱敏: %v", masked["database.password"])
	}
}

func TestMaskSensitiveContent_properties格式(t *testing.T) {
	parsed := parseAs(t, merge.FormatProperties, "db.password=abc123\nmax-players=100")
	masked := maskSensitiveContent(parsed, []string{"db.password"}).(map[string]any)
	if masked["db.password"] != ConfigMaskedPlaceholder {
		t.Fatalf("properties 敏感键未脱敏: %v", masked["db.password"])
	}
	if masked["max-players"] != "100" {
		t.Fatal("非敏感键不应被改动")
	}
}

func TestMaskSensitiveContent_null与子树不替换(t *testing.T) {
	parsed := parseAs(t, merge.FormatYAML, "secret: null\ntree:\n  a: 1")
	masked := maskSensitiveContent(parsed, []string{"secret", "tree"}).(map[string]any)
	if masked["secret"] != nil {
		t.Fatal("显式 null 删键指令不应被替换为占位符")
	}
	if _, isMap := masked["tree"].(map[string]any); !isMap {
		t.Fatal("命中子树不应整体替换")
	}
}

func TestMaskContentText_全链文本出口无明文(t *testing.T) {
	const sentinel = "P7-SENTINEL-9f2c"
	content := "api:\n  token: " + sentinel + "\nname: app\n"
	out, err := maskContentText(merge.FormatYAML, content, []string{"api.token"})
	if err != nil {
		t.Fatalf("脱敏失败: %v", err)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("脱敏输出仍含哨兵明文: %s", out)
	}
	if !strings.Contains(out, ConfigMaskedPlaceholder) {
		t.Fatalf("脱敏输出应含占位符: %s", out)
	}
}

func TestMaskContentText_空内容与无敏感路径原样(t *testing.T) {
	if out, err := maskContentText(merge.FormatYAML, "", []string{"a"}); err != nil || out != "" {
		t.Fatalf("空内容应原样返回: %q, %v", out, err)
	}
	if out, err := maskContentText(merge.FormatYAML, "a: 1\n", nil); err != nil || out != "a: 1\n" {
		t.Fatalf("无敏感路径应原样返回: %q, %v", out, err)
	}
}

func TestBackfill_占位符回填上一版明文(t *testing.T) {
	head := parseAs(t, merge.FormatYAML, "database:\n  password: real-secret\nname: app")
	submitted := parseAs(t, merge.FormatYAML, "database:\n  password: "+ConfigMaskedPlaceholder+"\nname: app2")
	if err := backfillSensitivePlaceholders(submitted, head, []string{"database.password"}); err != nil {
		t.Fatalf("回填失败: %v", err)
	}
	if submitted["database"].(map[string]any)["password"] != "real-secret" {
		t.Fatal("占位符未回填为上一版明文")
	}
}

func TestBackfill_提交新明文直接采用(t *testing.T) {
	head := parseAs(t, merge.FormatYAML, "token: old-secret")
	submitted := parseAs(t, merge.FormatYAML, "token: new-secret")
	if err := backfillSensitivePlaceholders(submitted, head, []string{"token"}); err != nil {
		t.Fatalf("非占位符不应报错: %v", err)
	}
	if submitted["token"] != "new-secret" {
		t.Fatal("新明文应直接采用")
	}
}

func TestBackfill_无上一版本可回填报错(t *testing.T) {
	cases := []struct {
		name string
		head any
	}{
		{"链空", nil},
		{"head 无该键", parseAs(t, merge.FormatYAML, "other: 1")},
		{"head 该键为 null", parseAs(t, merge.FormatYAML, "token: null")},
	}
	for _, tc := range cases {
		submitted := parseAs(t, merge.FormatYAML, "token: "+ConfigMaskedPlaceholder)
		err := backfillSensitivePlaceholders(submitted, tc.head, []string{"token"})
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code != "CONFIG_SENSITIVE_PLACEHOLDER_INVALID" {
			t.Errorf("%s: 应报 CONFIG_SENSITIVE_PLACEHOLDER_INVALID，得到 %v", tc.name, err)
		}
	}
}

func TestBackfill_回填后与直接提交明文哈希一致(t *testing.T) {
	// 验收 §7-8 前半：占位符回填落库后 hash 与直接提交明文一致
	head := parseAs(t, merge.FormatYAML, "database:\n  password: real-secret\nport: 3306")
	viaPlaceholder := parseAs(t, merge.FormatYAML, "database:\n  password: "+ConfigMaskedPlaceholder+"\nport: 3307")
	if err := backfillSensitivePlaceholders(viaPlaceholder, head, []string{"database.password"}); err != nil {
		t.Fatalf("回填失败: %v", err)
	}
	direct := parseAs(t, merge.FormatYAML, "database:\n  password: real-secret\nport: 3307")
	left, err := merge.Serialize(merge.FormatYAML, viaPlaceholder)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	right, err := merge.Serialize(merge.FormatYAML, direct)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if merge.Sha256Hex(left) != merge.Sha256Hex(right) {
		t.Fatalf("回填结果与直接提交明文哈希不一致:\n%s\nvs\n%s", left, right)
	}
}
