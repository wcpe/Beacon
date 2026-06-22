package gitexport

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/model"
)

// TestBuildPathConfigLayers FR-47：配置源层四层 + __GLOBAL__ 归一映射到确定性 git 路径。
func TestBuildPathConfigLayers(t *testing.T) {
	cases := []struct {
		name  string
		layer SourceLayer
		want  string
	}{
		{
			name: "global 层不嵌 group 段、__GLOBAL__ 渲染为 _global_",
			layer: SourceLayer{
				Kind: KindConfig, Namespace: "prod", Group: model.GlobalGroupCode,
				ScopeLevel: model.ScopeGlobal, Name: "mysql.yml",
			},
			want: "configs/prod/_global_/mysql.yml",
		},
		{
			name: "group 层自身格落 <group>/_group_",
			layer: SourceLayer{
				Kind: KindConfig, Namespace: "prod", Group: "area1",
				ScopeLevel: model.ScopeGroup, Name: "mysql.yml",
			},
			want: "configs/prod/area1/_group_/mysql.yml",
		},
		{
			name: "zone 层落 <group>/zone/<zone>",
			layer: SourceLayer{
				Kind: KindConfig, Namespace: "prod", Group: "area1",
				ScopeLevel: model.ScopeZone, ScopeTarget: "z1", Name: "mysql.yml",
			},
			want: "configs/prod/area1/zone/z1/mysql.yml",
		},
		{
			name: "server 层落 <group>/server/<serverId>",
			layer: SourceLayer{
				Kind: KindConfig, Namespace: "prod", Group: "area1",
				ScopeLevel: model.ScopeServer, ScopeTarget: "lobby-1", Name: "mysql.yml",
			},
			want: "configs/prod/area1/server/lobby-1/mysql.yml",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BuildPath(c.layer); got != c.want {
				t.Fatalf("BuildPath = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestBuildPathFileLayers FR-47：文件树源层落 files/，path 含子目录时保留层级。
func TestBuildPathFileLayers(t *testing.T) {
	layer := SourceLayer{
		Kind: KindFile, Namespace: "prod", Group: "area1",
		ScopeLevel: model.ScopeServer, ScopeTarget: "lobby-1", Name: "Demo/sub/config.yml",
	}
	want := "files/prod/area1/server/lobby-1/Demo/sub/config.yml"
	if got := BuildPath(layer); got != want {
		t.Fatalf("BuildPath = %q，期望 %q", got, want)
	}
}

// TestBuildPathSanitize FR-47：路径段防御性清洗——穿越 / 绝对前缀 / 反斜杠不得逃出导出仓。
func TestBuildPathSanitize(t *testing.T) {
	cases := []struct {
		name  string
		layer SourceLayer
		// 期望结果不含穿越、不以 / 开头、仍落在 configs/files 下
		wantPrefix string
		wantNoDots bool
	}{
		{
			name: "name 含 ../ 穿越被丢弃",
			layer: SourceLayer{
				Kind: KindFile, Namespace: "prod", Group: "area1",
				ScopeLevel: model.ScopeServer, ScopeTarget: "s1", Name: "../../etc/passwd",
			},
			wantPrefix: "files/prod/area1/server/s1/",
			wantNoDots: true,
		},
		{
			name: "name 绝对路径前缀被清洗",
			layer: SourceLayer{
				Kind: KindFile, Namespace: "prod", Group: "area1",
				ScopeLevel: model.ScopeGroup, Name: "/abs/path.yml",
			},
			wantPrefix: "files/prod/area1/_group_/",
			wantNoDots: true,
		},
		{
			name: "scope target 含分隔符被替换为下划线",
			layer: SourceLayer{
				Kind: KindConfig, Namespace: "prod", Group: "area1",
				ScopeLevel: model.ScopeServer, ScopeTarget: "a/b", Name: "x.yml",
			},
			wantPrefix: "configs/prod/area1/server/a_b/",
			wantNoDots: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildPath(c.layer)
			if !strings.HasPrefix(got, c.wantPrefix) {
				t.Fatalf("BuildPath = %q，应以 %q 开头", got, c.wantPrefix)
			}
			if strings.HasPrefix(got, "/") {
				t.Fatalf("BuildPath = %q，不应以 / 开头", got)
			}
			if c.wantNoDots && strings.Contains(got, "..") {
				t.Fatalf("BuildPath = %q，不应含 .. 穿越", got)
			}
		})
	}
}

// TestBuildSnapshotExcludesAndCiphertext FR-47：敏感排除文件不进快照、敏感配置项导密文原样。
func TestBuildSnapshotExcludesAndCiphertext(t *testing.T) {
	layers := []SourceLayer{
		{
			Kind: KindConfig, Namespace: "prod", Group: model.GlobalGroupCode,
			ScopeLevel: model.ScopeGlobal, Name: "plain.yml", Content: "a: 1\n",
		},
		{
			// 敏感配置项：Content 已是密文（导出层不解密），照常导出
			Kind: KindConfig, Namespace: "prod", Group: model.GlobalGroupCode,
			ScopeLevel: model.ScopeGlobal, Name: "redis.yml", Content: "enc:v1:Zm9vYmFy",
		},
		{
			// 文件树 path 级敏感排除：不进快照
			Kind: KindFile, Namespace: "prod", Group: "area1",
			ScopeLevel: model.ScopeServer, ScopeTarget: "s1", Name: "database.yml",
			Content: "password: secret123", Excluded: true,
		},
		{
			Kind: KindFile, Namespace: "prod", Group: "area1",
			ScopeLevel: model.ScopeGroup, Name: "messages.yml", Content: "hi: hello\n",
		},
	}
	snap := BuildSnapshot(layers)

	// 普通配置项明文导出
	if got := snap.Files["configs/prod/_global_/plain.yml"]; got != "a: 1\n" {
		t.Fatalf("普通配置项明文应原样导出，实际 %q", got)
	}
	// 敏感配置项导密文原样、绝不出现明文
	enc := snap.Files["configs/prod/_global_/redis.yml"]
	if enc != "enc:v1:Zm9vYmFy" {
		t.Fatalf("敏感配置项应导密文原样，实际 %q", enc)
	}
	if !strings.HasPrefix(enc, "enc:v1:") {
		t.Fatalf("敏感配置项导出内容应带 enc:v1: 前缀（密文），实际 %q", enc)
	}
	// 敏感排除文件不在快照里（git 看不到）
	if _, ok := snap.Files["files/prod/area1/server/s1/database.yml"]; ok {
		t.Fatal("敏感排除文件不应进快照（含明文密码）")
	}
	// 明文里绝不出现被排除文件的密码
	for p, c := range snap.Files {
		if strings.Contains(c, "secret123") {
			t.Fatalf("快照路径 %q 不应含被排除文件的明文密码", p)
		}
	}
	// 普通文件正常导出
	if got := snap.Files["files/prod/area1/_group_/messages.yml"]; got != "hi: hello\n" {
		t.Fatalf("普通文件应原样导出，实际 %q", got)
	}
	// 快照恰好 3 个文件（排除掉 1 个）
	if len(snap.Files) != 3 {
		t.Fatalf("快照应含 3 个文件（4 层排除 1），实际 %d", len(snap.Files))
	}
}

// TestBuildSnapshotEmpty FR-47：空源层得到空快照（不 panic）。
func TestBuildSnapshotEmpty(t *testing.T) {
	snap := BuildSnapshot(nil)
	if len(snap.Files) != 0 {
		t.Fatalf("空源层应得空快照，实际 %d", len(snap.Files))
	}
}

// TestBuildSnapshotDeterministic FR-47：相同源层两次组装得到逐一致快照（确定性）。
func TestBuildSnapshotDeterministic(t *testing.T) {
	layers := []SourceLayer{
		{Kind: KindConfig, Namespace: "prod", Group: "area1", ScopeLevel: model.ScopeZone, ScopeTarget: "z1", Name: "a.yml", Content: "x: 1"},
		{Kind: KindConfig, Namespace: "prod", Group: "area2", ScopeLevel: model.ScopeZone, ScopeTarget: "z2", Name: "b.yml", Content: "y: 2"},
	}
	s1 := BuildSnapshot(layers)
	s2 := BuildSnapshot(layers)
	if len(s1.Files) != len(s2.Files) {
		t.Fatalf("两次组装文件数不一致 %d vs %d", len(s1.Files), len(s2.Files))
	}
	for p, c := range s1.Files {
		if s2.Files[p] != c {
			t.Fatalf("路径 %q 两次内容不一致", p)
		}
	}
}
