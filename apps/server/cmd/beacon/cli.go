package main

import (
	"flag"
	"io"
)

// cliOptions 是控制面启动参数。
type cliOptions struct {
	configPath  string
	showVersion bool
}

// parseCLI 解析命令行；测试可注入参数与输出，避免污染全局 FlagSet。
func parseCLI(args []string, output io.Writer) (cliOptions, error) {
	set := flag.NewFlagSet("beacon", flag.ContinueOnError)
	set.SetOutput(output)
	var options cliOptions
	set.StringVar(&options.configPath, "config", "config.yml", "配置文件路径")
	set.BoolVar(&options.showVersion, "version", false, "输出版本号后退出")
	if err := set.Parse(args); err != nil {
		return cliOptions{}, err
	}
	return options, nil
}
