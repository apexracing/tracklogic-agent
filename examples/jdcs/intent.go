package jdcs

import (
	"context"
	"strings"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

type IntentClassifierTool struct {
	tool.BaseTool
}

func NewIntentClassifierTool() *IntentClassifierTool {
	return &IntentClassifierTool{
		BaseTool: tool.NewBaseTool(
			"classify_intent",
			"对用户输入进行意图分类，返回最匹配的意图类型和置信度。",
			[]model.ToolParameter{
				{Name: "input", Type: "string", Description: "用户输入文本", Required: true},
			},
		),
	}
}

func (t *IntentClassifierTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	input, _ := args["input"].(string)
	input = strings.ToLower(input)

	intent, confidence := classifyIntent(input)

	return map[string]any{
		"intent":     string(intent),
		"confidence": confidence,
		"input":      input,
	}, nil
}

func classifyIntent(input string) (IntentType, float64) {
	keywordPatterns := []struct {
		intent   IntentType
		keywords []string
		weight   float64
	}{
		{IntentGreeting, []string{"你好", "您好", "在吗", "hi", "hello", "嗨", "喂"}, 0.4},
		{IntentQueryOrder, []string{"订单", "买了", "下单", "购买", "查", "我的订单", "什么时候到"}, 0.5},
		{IntentTrackLogistics, []string{"物流", "快递", "配送", "发货", "送货", "运单", "顺丰", "到哪"}, 0.6},
		{IntentRefund, []string{"退款", "退货", "退钱", "不想要", "退货退款", "取消订单", "七天无理由"}, 0.7},
		{IntentRecommend, []string{"推荐", "推荐一下", "有什么", "哪个好", "买什么", "性价比", "推荐一款"}, 0.5},
		{IntentComplaint, []string{"投诉", "差评", "太差了", "垃圾", "不满意", "生气", "态度差"}, 0.6},
		{IntentTransfer, []string{"人工", "转人工", "客服", "找人工", "接人工", "人工客服", "活人"}, 0.8},
	}

	bestIntent := IntentUnknown
	bestScore := 0.0

	for _, pattern := range keywordPatterns {
		score := 0.0
		for _, kw := range pattern.keywords {
			if strings.Contains(input, kw) {
				score += pattern.weight
			}
		}
		if score > bestScore {
			bestScore = score
			bestIntent = pattern.intent
		}
	}

	confidence := bestScore
	if confidence > 0.95 {
		confidence = 0.95
	}
	if confidence < 0.1 {
		confidence = 0.1
	}

	return bestIntent, confidence
}
