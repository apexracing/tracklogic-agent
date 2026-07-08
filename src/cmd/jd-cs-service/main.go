package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go-harness-tutorial/internal/engine"
	"go-harness-tutorial/internal/examples/jd_cs"
	"go-harness-tutorial/internal/harness"
	"go-harness-tutorial/internal/security"
)

func main() {
	slog.Info("京东智能客服系统启动中...")

	cfg := harness.DefaultConfig()
	cfg.Name = "JD-CS-Service"
	cfg.LogLevel = "info"
	cfg.PermissionMode = "strict"

	cfg.DefaultModel = harness.ModelConfig{
		Provider: "openai",
		ModelID:  "gpt-4o-mini",
		Timeout:  60,
	}

	cfg.AllowedTools = []string{
		"calculator", "read_file", "write_file",
	}

	cfg.Security = harness.SecurityConfig{
		MaxInputLength:       5000,
		MaxOutputLength:      20000,
		EnableInjectionCheck: true,
		SanitizePII:          true,
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey != "" {
		cfg.DefaultModel.APIKey = apiKey
	} else {
		slog.Warn("OPENAI_API_KEY not set — using mock model for demo")
		cfg.DefaultModel.Provider = "mock"
		cfg.DefaultModel.ModelID = "mock-jd-cs"
	}

	h, err := harness.New(cfg)
	if err != nil {
		slog.Error("failed to create harness", "error", err)
		os.Exit(1)
	}
	defer h.Close()

	h.PermissionMgr.Allow(security.PermReadFile, security.PermNetAccess)

	slog.Info("注册客服组件...")
	triageAgent, orderAgent, refundAgent, err := jd_cs.SetupAgents(h)
	if err != nil {
		slog.Error("failed to setup agents", "error", err)
		os.Exit(1)
	}

	wf := jd_cs.BuildCSWorkflow(triageAgent, orderAgent, refundAgent)
	h.Workflows[wf.Name] = wf

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

func runWorkflowDemo(h *harness.Harness) {
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

func runBatchTests(h *harness.Harness) {
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

		output := h.RunAgent(context.Background(), test.agent, test.query, engine.WithMaxLoops(3))
		if output.Success {
			fmt.Printf("  客服: %s\n", truncate(output.Content, 300))
		} else {
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
