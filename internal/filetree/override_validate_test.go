package filetree

import "testing"

// TestValidateTargetRoot_Valid 合法目标根目录放行（限定 plugins/<plugin>/ 内的相对路径）。
func TestValidateTargetRoot_Valid(t *testing.T) {
	cases := []string{
		"plugins/AllinCore",
		"plugins/AllinCore/",
		"plugins/My-Plugin_1",
		"plugins/AllinCore/sub",
	}
	for _, c := range cases {
		if _, err := ValidateTargetRoot(c); err != nil {
			t.Errorf("期望放行 %q，却被拒：%v", c, err)
		}
	}
}

// TestValidateTargetRoot_Reject 非法目标根目录一律拒绝：穿越 / 绝对 / 盘符 / UNC / 非 plugins 前缀。
func TestValidateTargetRoot_Reject(t *testing.T) {
	cases := []string{
		"",                       // 空
		"AllinCore",              // 不在 plugins/ 下
		"plugins",                // 仅 plugins 根，未限定到具体插件
		"plugins/",               // 同上
		"plugins/..",             // 穿越回到根
		"plugins/../etc",         // 穿越
		"plugins/AllinCore/../..", // 穿越逃出 plugins
		"../plugins/AllinCore",   // 开头穿越
		"/plugins/AllinCore",     // 绝对路径
		"C:/plugins/AllinCore",   // Windows 盘符
		"C:plugins",              // Windows 盘符相对
		`plugins\AllinCore`,      // 反斜杠
		`\\host\share\plugins`,   // UNC
		"plugins/CON",            // Windows 保留设备名
		"plugins/aux",            // 保留设备名（小写）
		"plugins/lpt1",           // 保留设备名
		"plugins/Allin:Core",     // 含冒号（ADS / 盘符）
	}
	for _, c := range cases {
		if _, err := ValidateTargetRoot(c); err == nil {
			t.Errorf("期望拒绝 %q，却放行", c)
		}
	}
}

// TestValidateMemberPath_WithinRoot 成员文件相对 path 必须落在目标根内。
func TestValidateMemberPath_WithinRoot(t *testing.T) {
	root := "plugins/AllinCore"
	valid := []string{"config.yml", "scripts/hello.js", "ui-components/main.allin", "lang/zh_CN.yml"}
	for _, p := range valid {
		if err := ValidateMemberPath(root, p); err != nil {
			t.Errorf("期望放行成员 %q，却被拒：%v", p, err)
		}
	}
	invalid := []string{
		"",                  // 空
		"../escape.yml",     // 穿越逃出目标根
		"a/../../escape.yml", // 穿越逃出
		"/etc/passwd",       // 绝对
		`a\b.yml`,           // 反斜杠
		"C:/x.yml",          // 盘符
		"sub/CON",           // 保留设备名段
		"a:b.yml",           // 冒号
	}
	for _, p := range invalid {
		if err := ValidateMemberPath(root, p); err == nil {
			t.Errorf("期望拒绝成员 %q，却放行", p)
		}
	}
}

// TestValidateMemberPath_RejectJar 禁覆盖 .jar（防越界进 P3 发布编排，ADR-0011 决策 4）。
func TestValidateMemberPath_RejectJar(t *testing.T) {
	root := "plugins/AllinCore"
	for _, p := range []string{"AllinCore.jar", "libs/dep.JAR", "nested/x.Jar"} {
		if err := ValidateMemberPath(root, p); err == nil {
			t.Errorf("期望拒绝 jar 覆盖 %q，却放行", p)
		}
	}
}

// TestValidateMemberPath_RejectTrailingDotSpace 段尾点 / 空格会被 Windows 落盘剥离，
// 借此绕过 .jar / 保留名 / server 文件禁覆盖，必须拒（与 agent OverridePathSecurity 同口径）。
func TestValidateMemberPath_RejectTrailingDotSpace(t *testing.T) {
	root := "plugins/AllinCore"
	cases := []string{
		"AllinCore.jar.",   // 尾点绕过 .jar
		"nested/x.jar.",    // 子目录尾点绕过 .jar
		"sub /x.yml",       // 非末段尾空格
		"con /x.yml",       // 尾空格绕过保留名
		"plain.yml.",       // 尾点
	}
	for _, p := range cases {
		if err := ValidateMemberPath(root, p); err == nil {
			t.Errorf("期望拒绝尾随点/空格成员 %q，却放行", p)
		}
	}
}

// TestValidateReloadCommand_Allow 合法单条重载命令放行（不含元字符）。
func TestValidateReloadCommand_Allow(t *testing.T) {
	cases := []string{
		"allin reload",
		"reload confirm",
		"papi reload",
		"lp sync",
	}
	for _, c := range cases {
		if _, err := ValidateReloadCommand(c); err != nil {
			t.Errorf("期望放行命令 %q，却被拒：%v", c, err)
		}
	}
}

// TestValidateReloadCommand_RejectInjection 命令注入字符一律拒绝（ADR-0011 决策 3）。
func TestValidateReloadCommand_RejectInjection(t *testing.T) {
	cases := []string{
		"",                       // 空
		"   ",                    // 全空白
		"reload; rm -rf /",       // 分号
		"reload && evil",         // 与
		"reload || evil",         // 或
		"reload | cat",           // 管道
		"reload > out",           // 重定向
		"reload < in",            // 重定向
		"reload $HOME",           // 变量展开
		"reload\nplay evil",      // 换行
		"reload\rplay evil",      // 回车
		"reload `whoami`",        // 反引号
		"reload\aevil",       // 控制字符（响铃）
		"reload	play",           // 制表符（控制字符）
		"reload &",               // 后台符
	}
	for _, c := range cases {
		if _, err := ValidateReloadCommand(c); err == nil {
			t.Errorf("期望拒绝命令 %q，却放行", c)
		}
	}
}

// TestValidateReloadCommand_FirstToken 返回归一化命令与首 token（供白名单校验用）。
func TestValidateReloadCommand_FirstToken(t *testing.T) {
	norm, err := ValidateReloadCommand("  allin   reload  ")
	if err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	if norm != "allin   reload" {
		t.Errorf("命令应去首尾空白，得 %q", norm)
	}
	if tok := FirstToken(norm); tok != "allin" {
		t.Errorf("首 token 应为 allin，得 %q", tok)
	}
}
