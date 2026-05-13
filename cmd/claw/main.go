package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/chef-guo/agents-hive/internal/cli"
	"github.com/chef-guo/agents-hive/internal/config"
)

func main() {
	opts := cli.ParseArgs(os.Args[1:])

	cfg, err := config.LoadCLI(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置错误: %v\n", err)
		os.Exit(1)
	}

	// CLI 标志覆盖所有配置（最高优先级）
	cfg.ApplyOverrides(opts.Model, opts.BaseURL, opts.APIKey, opts.LogLevel)

	// --hitl 标志启用 HITL 模式
	if opts.HITL {
		cfg.HITL.Enabled = true
	}

	// --verbose 标志覆盖控制台日志级别，显示所有日志
	if opts.Verbose {
		cfg.Logging.ConsoleLevel = "debug"
	}

	logger, err := cfg.NewLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建日志器错误: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	app := cli.NewApp(cfg, logger)
	defer app.Close()

	// --acp 模式：以 ACP 协议启动，供 IDE 零配置接入
	if opts.ACP {
		// ACP 模式下控制台日志强制输出到 stderr（避免污染 stdio JSON-RPC 通道）
		cfg.Logging.ConsoleLevel = "error"
		if err := app.RunACP(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "ACP 错误: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// 非交互模式可以通过 echo "query" | claw 实现
	if err := app.RunInteractive(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}
