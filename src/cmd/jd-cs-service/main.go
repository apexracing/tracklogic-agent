package main

import (
	"context"
	"encoding/json"
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
		slog.Warn("OPENAI_API_KEY not set — will use mock model fallback")
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
	_ = wf
	_ = orderAgent

	slog.Info("京东智能客服系统就绪")

	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  京东智能客服系统 v1.0 — 模拟会话演示")
	fmt.Println("═══════════════════════════════════════")

	runTriageDemo(h)

	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  场景批处理测试")
	fmt.Println("═══════════════════════════════════════")

	runBatchTests(h)

	fmt.Println("\n═══════════════════════════════════════")
	fmt.Println("  Demo 完成。")
	fmt.Println("═══════════════════════════════════════")
}

func runTriageDemo(h *harness.Harness) {
	queries := []string{
		"你好，帮我查一下订单 ord1001 的情况",
		"我想退货退款，订单是 ord1005，显示器有个坏点",
		"帮我看看快递 SF1234567890 到哪了",
		"有什么好用的机械键盘推荐吗？",
	}

	for _, q := range queries {
		fmt.Printf("\n▎ 用户: %s\n", q)
		output := h.RunAgent(context.Background(), "triage_agent", q, engine.WithMaxLoops(3))
		if output.Success {
			safeContent := output.Content
			if h.Config.Security.SanitizePII {
				safeContent = h.Sanitize(safeContent)
			}
			fmt.Printf("▎ 客服: %s\n", safeContent)
		} else {
			fmt.Printf("▎ 客服: （处理中...）%s\n", output.Error)
		}
		fmt.Printf("  ── loop:%d tokens:%d\n", output.LoopCount, output.TotalTokens)
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
			content := output.Content
			if h.Config.Security.SanitizePII {
				content = h.Sanitize(content)
			}
			fmt.Printf("  客服: %s\n", truncate(content, 300))
		} else {
			fmt.Printf("  ✗ 失败: %s\n", output.Error)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func toJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

var _ = json.MarshalIndent
