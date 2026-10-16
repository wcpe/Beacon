package version

import "testing"

// TestDisplay 覆盖候选标记与构建提交的四种组合：这是「同一正式版本的不同候选可辨」的唯一判据。
func TestDisplay(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		candidate string
		commit    string
		want      string
	}{
		{name: "无候选无提交（本地构建）", version: "1.5.0", want: "1.5.0"},
		{name: "仅候选（RC）", version: "1.5.0", candidate: "rc.2", want: "1.5.0-rc.2"},
		{name: "仅提交", version: "1.5.0", commit: "114cf533", want: "1.5.0 (114cf533)"},
		{name: "候选与提交（发布构建）", version: "1.5.0", candidate: "rc.2", commit: "114cf533", want: "1.5.0-rc.2 (114cf533)"},
		{name: "哨兵值不加修饰", version: "dev", want: "dev"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			originalVersion, originalCandidate, originalCommit := Version, Candidate, Commit
			t.Cleanup(func() { Version, Candidate, Commit = originalVersion, originalCandidate, originalCommit })
			Version, Candidate, Commit = testCase.version, testCase.candidate, testCase.commit
			if got := Display(); got != testCase.want {
				t.Fatalf("Display() = %q，期望 %q", got, testCase.want)
			}
		})
	}
}

// TestDisplayDistinguishesCandidates 锁定本改动要解决的问题：同一正式版本的两个候选必须输出不同串。
func TestDisplayDistinguishesCandidates(t *testing.T) {
	originalVersion, originalCandidate, originalCommit := Version, Candidate, Commit
	t.Cleanup(func() { Version, Candidate, Commit = originalVersion, originalCandidate, originalCommit })
	Version, Commit = "1.5.0", "114cf533"

	Candidate = "rc.1"
	first := Display()
	Candidate = "rc.2"
	second := Display()

	if first == second {
		t.Fatalf("rc.1 与 rc.2 的版本串相同：%q", first)
	}
	if first != "1.5.0-rc.1 (114cf533)" || second != "1.5.0-rc.2 (114cf533)" {
		t.Fatalf("候选串不符合预期：rc.1=%q rc.2=%q", first, second)
	}
}

// TestVersionStaysPlainSemver 守住 Version 字段的契约：在线更新解析依赖三段纯数字，禁止把候选标记拼进去。
func TestVersionStaysPlainSemver(t *testing.T) {
	originalVersion, originalCandidate := Version, Candidate
	t.Cleanup(func() { Version, Candidate = originalVersion, originalCandidate })
	Version, Candidate = "1.5.0", "rc.2"

	if Version != "1.5.0" {
		t.Fatalf("Version 字段被污染为 %q，在线更新解析将失效", Version)
	}
}
