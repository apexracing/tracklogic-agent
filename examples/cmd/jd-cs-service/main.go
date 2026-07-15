package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	agent "github.com/apexracing/tracklogic-agent"
	"github.com/apexracing/tracklogic-agent/examples/jd_cs"
)

// 写死加载的配置文件（相对运行目录：请在仓库根目录执行 go run）
const configFile = "examples/config.example.json"
const apiKeyEnv = "TRACKLOGIC_AGENT_API_KEY"

func main() {
	slog.Info("京东智能客服系统启动中...")

	cfg, err := agent.LoadConfig(configFile)
	if err != nil {
		slog.Error("failed to load config", "path", configFile, "error", err)
		os.Exit(1)
	}

	cfg.Name = "JD-CS-Service"
	if apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv)); apiKey != "" {
		cfg.DefaultModel.APIKey = apiKey
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	cfg.PermissionMode = "strict"

	agent.ResolveModelDefaults(&cfg.DefaultModel)

	// api_key 为空时回退 mock，便于本地无 Key 演示
	if cfg.DefaultModel.APIFormat != "mock" && cfg.DefaultModel.APIKey == "" {
		slog.Warn("api_key empty — falling back to mock",
			"vendor", cfg.DefaultModel.Vendor,
			"api_format", cfg.DefaultModel.APIFormat)
		cfg.DefaultModel.APIFormat = "mock"
		if cfg.DefaultModel.ModelID == "" {
			cfg.DefaultModel.ModelID = "mock-jd-cs"
		}
	}

	slog.Info("model config",
		"vendor", cfg.DefaultModel.Vendor,
		"api_format", cfg.DefaultModel.APIFormat,
		"base_url", cfg.DefaultModel.BaseURL,
		"model_id", cfg.DefaultModel.ModelID,
		"config", configFile,
	)

	h, err := agent.New(cfg)
	if err != nil {
		slog.Error("failed to create harness", "error", err)
		os.Exit(1)
	}
	defer h.Close()

	h.AllowPermissions(agent.PermReadFile, agent.PermNetAccess)

	slog.Info("注册客服组件...")
	triageAgent, orderAgent, refundAgent, err := jd_cs.SetupAgents(h)
	if err != nil {
		slog.Error("failed to setup agents", "error", err)
		os.Exit(1)
	}

	wf := jd_cs.BuildCSWorkflow(triageAgent, orderAgent, refundAgent)
	if err := h.RegisterWorkflow(wf); err != nil {
		slog.Error("failed to register workflow", "error", err)
		os.Exit(1)
	}

	slog.Info("京东智能客服系统就绪")

	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  京东智能客服系统 v1.0 — 工作流演示")
	fmt.Println("═══════════════════════════════════════")

	runWorkflowDemo(h)

	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  单 Agent 批处理测试")
	fmt.Println("═══════════════════════════════════════")

	runBatchTests(h)

	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  Demo 完成。")
	fmt.Println("═══════════════════════════════════════")
}

func runWorkflowDemo(h *agent.Harness) {
	queries := []string{
		"你好，帮我查一下订单 ord1001 的情况",
		"我想退货退款，订单是 ord1005，显示器有个坏点",
		"帮我看看快递 SF1234567890 到哪了",
		"有什么好用的机械键盘推荐吗？",
	}

	for _, q := range queries {
		fmt.Printf("\n▎ 用户: %s\n", q)
		result := h.RunWorkflow(context.Background(), "京东智能客服工作流", q)
		if result.Success {
			fmt.Printf("▎ 客服: %s\n", truncate(result.Output, 500))
		} else {
			fmt.Printf("▎ 客服: （处理失败）%s\n", result.Error)
		}
		if intent, ok := result.State["intent"]; ok {
			fmt.Printf("  ── intent:%v steps:%d duration:%s\n", intent, len(result.StepLogs), result.Duration)
		}
	}
}

func runBatchTests(h *agent.Harness) {
	tests := []struct {
		agent string
		query string
	}{
		{"order_agent", "我的订单 ord1002 什么时候到？查一下物流"},
		{"order_agent", "我买了两副 AirPods，订单是 ord1003，能改地址吗"},
		{"order_agent", "我想投诉，快递太慢了"},
	}

	for i, test := range tests {
		fmt.Printf("\n▸ 场景 %d [%s]\n", i+1, test.agent)
		fmt.Printf("  用户: %s\n", test.query)
		fmt.Print("  客服: ")

		output := h.RunAgent(context.Background(), test.agent, test.query,
			agent.WithMaxLoops(5),
			agent.WithStream(func(chunk string) { fmt.Print(chunk) }),
		)
		fmt.Println()
		if !output.Success {
			fmt.Printf("  ✗ 失败: %s\n", output.Error)
		}
	}
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
