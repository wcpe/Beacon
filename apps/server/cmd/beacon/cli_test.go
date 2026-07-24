package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/version"
)

func TestParseCLIVersion(t *testing.T) {
	var output bytes.Buffer
	options, err := parseCLI([]string{"--version"}, &output)
	if err != nil {
		t.Fatalf("解析 --version 失败：%v", err)
	}
	if !options.showVersion {
		t.Fatal("--version 应启用版本输出")
	}
	if options.configPath != "config.yml" {
		t.Fatalf("默认配置路径错误：%q", options.configPath)
	}
}

// TestVersionCommandOutputsVersionAndExitsZero 验证真实 run 入口只输出版本并以零状态退出。
func TestVersionCommandOutputsVersionAndExitsZero(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestVersionCommandHelper$", "--")
	cmd.Env = append(os.Environ(), "BEACON_CLI_VERSION_HELPER=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("beacon --version 应以零状态退出：%v，stderr=%q", err, stderr.String())
	}
	if got := stdout.String(); got != "1.0.0\n" {
		t.Fatalf("beacon --version stdout 应为 1.0.0，实际 %q", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("beacon --version 不应写 stderr，实际 %q", got)
	}
}

// TestVersionCommandHelper 在子进程中执行真实 run，避免测试进程被退出行为终止。
func TestVersionCommandHelper(_ *testing.T) {
	if os.Getenv("BEACON_CLI_VERSION_HELPER") != "1" {
		return
	}
	version.Version = "1.0.0"
	os.Args = []string{"beacon", "--version"}
	if err := run(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestParseCLIConfig(t *testing.T) {
	var output bytes.Buffer
	options, err := parseCLI([]string{"--config", "custom.yml"}, &output)
	if err != nil {
		t.Fatalf("解析 --config 失败：%v", err)
	}
	if options.showVersion || options.configPath != "custom.yml" {
		t.Fatalf("解析结果错误：%+v", options)
	}
}
