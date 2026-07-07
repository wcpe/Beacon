package redact

import "testing"

func TestDesensitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// URL 里的 user:pass@ 密码段打码（保留用户名便于辨识）
		{
			name: "URL 凭据打码",
			in:   `下载失败: Get "http://admin:s3cr3t@10.0.0.5:7890": connection refused`,
			want: `下载失败: Get "http://admin:***@10.0.0.5:7890": connection refused`,
		},
		// token=xxx 键值打码
		{
			name: "token 键值打码",
			in:   `请求失败 url=https://api/x?token=abc123def&page=1`,
			want: `请求失败 url=https://api/x?token=***&page=1`,
		},
		// password=xxx 打码（大小写不敏感）
		{
			name: "password 键值打码",
			in:   `dsn 解析失败: user=root Password=MyP@ss host=db`,
			want: `dsn 解析失败: user=root Password=*** host=db`,
		},
		// secret / api-key 打码
		{
			name: "secret 与 api-key 打码",
			in:   `secret=topsecret api_key=KEY-999`,
			want: `secret=*** api_key=***`,
		},
		// Bearer 令牌打码
		{
			name: "Bearer 令牌打码",
			in:   `鉴权失败 Authorization: Bearer eyJhbGci.payload.sig`,
			want: `鉴权失败 Authorization: Bearer ***`,
		},
		// 内网地址 / 主机名 / 文件路径属运维上下文，不打码（ADR-0057）
		{
			name: "内网地址与路径保留",
			in:   `写临时文件失败: open /var/lib/beacon/beacon.new: no space, host=192.168.1.10:8848`,
			want: `写临时文件失败: open /var/lib/beacon/beacon.new: no space, host=192.168.1.10:8848`,
		},
		// 无敏感片段原样返回
		{
			name: "无敏感原样",
			in:   `下载资产失败: 写临时文件失败: context canceled`,
			want: `下载资产失败: 写临时文件失败: context canceled`,
		},
		// 空串
		{
			name: "空串",
			in:   ``,
			want: ``,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Desensitize(c.in); got != c.want {
				t.Errorf("Desensitize(%q)\n  得到 %q\n  期望 %q", c.in, got, c.want)
			}
		})
	}
}
