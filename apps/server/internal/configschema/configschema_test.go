package configschema

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/merge"
)

// mustCompile 编译 schema，失败即 Fatal。
func mustCompile(t *testing.T, format, schemaJSON string) *Validator {
	t.Helper()
	v, err := Compile(format, schemaJSON)
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	return v
}

// parseYAML 解析 yaml 内容，失败即 Fatal。
func parseYAML(t *testing.T, content string) any {
	t.Helper()
	parsed, err := merge.Parse(merge.FormatYAML, content)
	if err != nil {
		t.Fatalf("解析 yaml 失败: %v", err)
	}
	return parsed
}

// hasViolationAt 判断违例列表中是否存在指定 path 且 message 含子串的违例。
func hasViolationAt(list []Violation, path, msgPart string) bool {
	for _, v := range list {
		if v.Path == path && strings.Contains(v.Message, msgPart) {
			return true
		}
	}
	return false
}

// ---- schema 本身合法性（保存 schema_json 时先行拒绝）----

func TestCompile_非法JSON被拒(t *testing.T) {
	if _, err := Compile(merge.FormatYAML, `{"type":`); err == nil {
		t.Fatal("非法 JSON 的 schema 应编译失败")
	}
}

func TestCompile_不符合元Schema被拒(t *testing.T) {
	cases := []string{
		`{"type": 5}`,          // type 取值非法
		`[1, 2]`,               // schema 顶层必须是对象 / 布尔
		`{"pattern": 123}`,     // pattern 必须是字符串
		`{"minimum": "large"}`, // minimum 必须是数字
	}
	for _, schema := range cases {
		if _, err := Compile(merge.FormatYAML, schema); err == nil {
			t.Errorf("schema %s 应编译失败", schema)
		}
	}
}

func TestCompile_非法正则被拒(t *testing.T) {
	if _, err := Compile(merge.FormatYAML, `{"properties":{"x":{"pattern":"([a-"}}}`); err == nil {
		t.Fatal("pattern 非法正则应编译失败")
	}
	if _, err := Compile(merge.FormatProperties, `{"properties":{"x":{"pattern":"([a-"}}}`); err == nil {
		t.Fatal("properties 格式 pattern 非法正则应编译失败")
	}
}

// ---- 规格九关键字逐一覆盖（yaml/json 路径）----

func TestValidate_type(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"n":{"type":"integer"},"s":{"type":"string"},"b":{"type":"boolean"}}}`)
	if got := v.Validate(parseYAML(t, "n: 1\ns: hi\nb: true"), false); len(got) != 0 {
		t.Fatalf("类型全对应通过，得到 %v", got)
	}
	got := v.Validate(parseYAML(t, "n: abc\nb: 3"), false)
	if !hasViolationAt(got, "n", "want integer") || !hasViolationAt(got, "b", "want boolean") {
		t.Fatalf("类型违例缺失: %v", got)
	}
}

func TestValidate_properties只校验出现的键(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"a":{"type":"integer"},"b":{"type":"string"}}}`)
	// 只提交 a：b 未出现不校验（部分校验语义）
	if got := v.Validate(parseYAML(t, "a: 1"), false); len(got) != 0 {
		t.Fatalf("未出现的键不应校验，得到 %v", got)
	}
}

func TestValidate_required只在基线层强制(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"type":"object","required":["host","port"],"properties":{"host":{"type":"string"}}}`)
	// 非基线层：缺 required 放行
	if got := v.Validate(parseYAML(t, "host: db.local"), false); len(got) != 0 {
		t.Fatalf("非基线层不应强制 required，得到 %v", got)
	}
	// 基线层：缺 port 违例
	got := v.Validate(parseYAML(t, "host: db.local"), true)
	if !hasViolationAt(got, "(root)", "port") {
		t.Fatalf("基线层缺 required 应违例: %v", got)
	}
	// 基线层齐全通过
	if got := v.Validate(parseYAML(t, "host: db.local\nport: x"), true); len(got) != 0 {
		t.Fatalf("required 齐全应通过，得到 %v", got)
	}
}

func TestValidate_嵌套required剥除不误伤同名属性(t *testing.T) {
	// 属性名恰为 required：剥除只发生在 schema 关键字位，不动 properties 键
	v := mustCompile(t, merge.FormatYAML,
		`{"required":["top"],"properties":{"required":{"type":"integer"},"nest":{"type":"object","required":["inner"],"properties":{"inner":{"type":"string"}}}}}`)
	// 非基线：顶层与嵌套 required 均不强制，但名为 required 的属性类型仍校验
	got := v.Validate(parseYAML(t, "required: abc\nnest:\n  other: 1"), false)
	if !hasViolationAt(got, "required", "want integer") {
		t.Fatalf("名为 required 的属性类型校验应保留: %v", got)
	}
	for _, item := range got {
		if strings.Contains(item.Message, "missing property") {
			t.Fatalf("非基线层不应有 required 违例: %v", got)
		}
	}
	// 基线：嵌套 required 也强制
	got = v.Validate(parseYAML(t, "top: 1\nrequired: 2\nnest:\n  other: 1"), true)
	if !hasViolationAt(got, "nest", "inner") {
		t.Fatalf("基线层嵌套 required 应强制: %v", got)
	}
}

func TestValidate_enum(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"mode":{"enum":["fast","safe"]},"level":{"enum":[1,2,3]}}}`)
	if got := v.Validate(parseYAML(t, "mode: fast\nlevel: 2"), false); len(got) != 0 {
		t.Fatalf("枚举命中应通过，得到 %v", got)
	}
	got := v.Validate(parseYAML(t, "mode: turbo\nlevel: 9"), false)
	if !hasViolationAt(got, "mode", "one of") || !hasViolationAt(got, "level", "one of") {
		t.Fatalf("枚举违例缺失: %v", got)
	}
}

func TestValidate_minimum_maximum(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"n":{"minimum":1,"maximum":10}}}`)
	if got := v.Validate(parseYAML(t, "n: 5"), false); len(got) != 0 {
		t.Fatalf("范围内应通过，得到 %v", got)
	}
	if got := v.Validate(parseYAML(t, "n: 0"), false); !hasViolationAt(got, "n", "minimum") {
		t.Fatalf("低于 minimum 应违例: %v", got)
	}
	if got := v.Validate(parseYAML(t, "n: 11"), false); !hasViolationAt(got, "n", "maximum") {
		t.Fatalf("高于 maximum 应违例: %v", got)
	}
}

func TestValidate_pattern(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"id":{"type":"string","pattern":"^[a-z]+-\\d+$"}}}`)
	if got := v.Validate(parseYAML(t, "id: lobby-1"), false); len(got) != 0 {
		t.Fatalf("匹配 pattern 应通过，得到 %v", got)
	}
	if got := v.Validate(parseYAML(t, "id: Lobby_1"), false); !hasViolationAt(got, "id", "pattern") {
		t.Fatalf("不匹配 pattern 应违例: %v", got)
	}
}

func TestValidate_items(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"list":{"type":"array","items":{"type":"string"}}}}`)
	if got := v.Validate(parseYAML(t, "list:\n  - a\n  - b"), false); len(got) != 0 {
		t.Fatalf("元素类型全对应通过，得到 %v", got)
	}
	if got := v.Validate(parseYAML(t, "list:\n  - a\n  - 3"), false); !hasViolationAt(got, "list.1", "want string") {
		t.Fatalf("元素类型违例缺失: %v", got)
	}
}

func TestValidate_additionalProperties(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"a":{"type":"integer"}},"additionalProperties":false}`)
	if got := v.Validate(parseYAML(t, "a: 1"), false); len(got) != 0 {
		t.Fatalf("无额外键应通过，得到 %v", got)
	}
	if got := v.Validate(parseYAML(t, "a: 1\nextra: x"), false); !hasViolationAt(got, "(root)", "extra") {
		t.Fatalf("额外键应违例: %v", got)
	}
}

// ---- 部分校验特殊语义 ----

func TestValidate_显式null删键指令跳过类型校验(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"required":["a"],"properties":{"a":{"type":"integer"},"nest":{"type":"object","properties":{"b":{"type":"string"}}}}}`)
	// a 显式 null（删键指令）：非基线层跳过类型校验
	if got := v.Validate(parseYAML(t, "a: null\nnest:\n  b: null"), false); len(got) != 0 {
		t.Fatalf("显式 null 删键应跳过校验，得到 %v", got)
	}
	// 基线层：a 为 null 视为键不存在 → required 违例（基线层的 null 即基线里无此键）
	if got := v.Validate(parseYAML(t, "a: null"), true); !hasViolationAt(got, "(root)", "a") {
		t.Fatalf("基线层 null 键应触发 required 违例: %v", got)
	}
}

func TestValidate_list内null是普通元素(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"properties":{"list":{"items":{"type":"string"}}}}`)
	if got := v.Validate(parseYAML(t, "list:\n  - a\n  - null"), false); !hasViolationAt(got, "list.1", "want string") {
		t.Fatalf("list 内 null 应按元素值校验: %v", got)
	}
}

func TestValidate_空层不贡献直接通过(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML, `{"required":["a"]}`)
	if got := v.Validate(nil, true); got != nil {
		t.Fatalf("空层应直接通过，得到 %v", got)
	}
}

// ---- properties 格式扁平校验 ----

func TestValidate_properties格式_pattern与enum(t *testing.T) {
	v := mustCompile(t, merge.FormatProperties,
		`{"required":["max-players"],"properties":{"max-players":{"pattern":"^\\d+$"},"gamemode":{"enum":["survival","creative"]}}}`)
	parsed, err := merge.Parse(merge.FormatProperties, "max-players=abc\ngamemode=hardcore\nfree-key=anything")
	if err != nil {
		t.Fatalf("解析 properties 失败: %v", err)
	}
	got := v.Validate(parsed, false)
	if !hasViolationAt(got, "max-players", "pattern") || !hasViolationAt(got, "gamemode", "one of") {
		t.Fatalf("扁平 pattern/enum 违例缺失: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("未定义键应放行，违例应恰 2 条: %v", got)
	}
}

func TestValidate_properties格式_required仅基线层(t *testing.T) {
	v := mustCompile(t, merge.FormatProperties, `{"required":["max-players"],"properties":{}}`)
	parsed, err := merge.Parse(merge.FormatProperties, "pvp=true")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := v.Validate(parsed, false); len(got) != 0 {
		t.Fatalf("非基线层不强制 required: %v", got)
	}
	if got := v.Validate(parsed, true); !hasViolationAt(got, "(root)", "max-players") {
		t.Fatalf("基线层缺 required 应违例: %v", got)
	}
}

func TestValidate_properties格式_数字枚举按字符串比对(t *testing.T) {
	v := mustCompile(t, merge.FormatProperties, `{"properties":{"view-distance":{"enum":[6,8,10]}}}`)
	ok, err := merge.Parse(merge.FormatProperties, "view-distance=8")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := v.Validate(ok, false); len(got) != 0 {
		t.Fatalf("字符串化枚举应命中: %v", got)
	}
	bad, _ := merge.Parse(merge.FormatProperties, "view-distance=7")
	if got := v.Validate(bad, false); !hasViolationAt(got, "view-distance", "one of") {
		t.Fatalf("枚举未命中应违例: %v", got)
	}
}

// ---- 违例形状 ----

func TestValidate_违例含路径与原因且嵌套路径点分(t *testing.T) {
	v := mustCompile(t, merge.FormatYAML,
		`{"properties":{"database":{"type":"object","properties":{"port":{"type":"integer"}}}}}`)
	got := v.Validate(parseYAML(t, "database:\n  port: abc"), false)
	if len(got) != 1 || got[0].Path != "database.port" || got[0].Message == "" {
		t.Fatalf("嵌套违例路径应为点分 database.port 且带原因: %v", got)
	}
}
