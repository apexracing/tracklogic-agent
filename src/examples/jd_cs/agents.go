package jd_cs

import (
	"go-harness-tutorial/internal/engine"
	"go-harness-tutorial/internal/harness"
	"go-harness-tutorial/internal/tool"
)

func SetupAgents(h *harness.Harness) (*engine.Agent, *engine.Agent, *engine.Agent, error) {
	triageTool := NewIntentClassifierTool()
	if err := h.RegisterTool(triageTool); err != nil {
		return nil, nil, nil, err
	}

	csTools := []tool.Tool{
		NewGetUserInfoTool(), NewQueryOrderTool(), NewListOrdersByUserTool(),
		NewTrackLogisticsTool(), NewRecommendProductTool(), NewCreateRefundTool(),
		NewTransferHumanTool(), NewSendCouponTool(),
	}
	for _, t := range csTools {
		if err := h.RegisterTool(t); err != nil {
			return nil, nil, nil, err
		}
	}

	triageAgent := h.NewAgent("triage_agent",
		`你是京东智能客服的分流系统。
你的职责是分析用户输入，判断用户意图。

可识别意图：
- query_order: 查询订单信息
- track_logistics: 查询物流
- refund: 退款退货
- recommend_product: 商品推荐
- complaint: 投诉/不满
- greeting: 问候/开场白
- transfer_human: 用户要求转人工
- unknown: 无法识别

分析用户输入后，调用 classify_intent 工具返回分类结果。`,
	)

	orderAgent := h.NewAgent("order_agent",
		`你是京东智能客服的订单专员。
你可以查询订单信息、物流状态。
如果用户需要退款，转交给退款专员。
如果用户不满，可以发放优惠券安抚。
如果无法处理，转人工客服。

请使用工具获取数据后，用中文友好回答。`,
	)

	refundAgent := h.NewAgent("refund_agent",
		`你是京东智能客服的退款专员。
你处理用户的退款、退货申请。
注意：
- 只有已收货的订单才能申请退款
- 退款金额不能超过订单金额
- 创建退款后告知用户审核时间（1-3个工作日）

如果用户不满，可以配合发优惠券安抚。
无法处理的请转人工。`,
	)

	return triageAgent, orderAgent, refundAgent, nil
}
