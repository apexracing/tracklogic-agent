package jdcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

type GetUserInfoTool struct {
	tool.BaseTool
}

func NewGetUserInfoTool() *GetUserInfoTool {
	return &GetUserInfoTool{
		BaseTool: tool.NewBaseTool(
			"get_user_info",
			"查询用户信息，包括会员等级、积分等。",
			[]model.ToolParameter{
				{Name: "user_id", Type: "string", Description: "用户ID", Required: true},
			},
		),
	}
}

func (t *GetUserInfoTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	userID, _ := args["user_id"].(string)
	user, ok := MockUsers[userID]
	if !ok {
		return nil, fmt.Errorf("用户 %s 不存在", userID)
	}
	return map[string]any{
		"user_id": user.UserID, "name": user.Name,
		"level": user.Level, "points": user.Points,
		"join_date": user.JoinDate,
	}, nil
}

type QueryOrderTool struct {
	tool.BaseTool
}

func NewQueryOrderTool() *QueryOrderTool {
	return &QueryOrderTool{
		BaseTool: tool.NewBaseTool(
			"query_order",
			"查询订单信息，包括订单状态、商品、金额等。",
			[]model.ToolParameter{
				{Name: "order_id", Type: "string", Description: "订单号", Required: true},
			},
		),
	}
}

func (t *QueryOrderTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	orderID, _ := args["order_id"].(string)
	order, ok := MockOrders[orderID]
	if !ok {
		return nil, fmt.Errorf("订单 %s 不存在", orderID)
	}
	return map[string]any{
		"order_id": order.OrderID, "product_name": order.ProductName,
		"price": order.Price, "quantity": order.Quantity,
		"status": order.Status, "created_at": order.CreatedAt.Format("2006-01-02 15:04"),
		"address": order.Address, "tracking_number": order.TrackingNum,
	}, nil
}

type ListOrdersByUserTool struct {
	tool.BaseTool
}

func NewListOrdersByUserTool() *ListOrdersByUserTool {
	return &ListOrdersByUserTool{
		BaseTool: tool.NewBaseTool(
			"list_user_orders",
			"查询用户的所有订单列表。",
			[]model.ToolParameter{
				{Name: "user_id", Type: "string", Description: "用户ID", Required: true},
			},
		),
	}
}

func (t *ListOrdersByUserTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	userID, _ := args["user_id"].(string)
	var orders []map[string]any
	for _, order := range MockOrders {
		if order.UserID == userID {
			orders = append(orders, map[string]any{
				"order_id": order.OrderID, "product_name": order.ProductName,
				"price": order.Price, "quantity": order.Quantity,
				"status": order.Status, "created_at": order.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
	}
	if orders == nil {
		return []string{}, nil
	}
	return orders, nil
}

type TrackLogisticsTool struct {
	tool.BaseTool
}

func NewTrackLogisticsTool() *TrackLogisticsTool {
	return &TrackLogisticsTool{
		BaseTool: tool.NewBaseTool(
			"track_logistics",
			"查询物流信息，包括当前状态和运输轨迹。",
			[]model.ToolParameter{
				{Name: "tracking_number", Type: "string", Description: "快递单号", Required: true},
			},
		),
	}
}

func (t *TrackLogisticsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	trackingNum, _ := args["tracking_number"].(string)
	info, ok := MockLogistics[trackingNum]
	if !ok {
		return nil, fmt.Errorf("物流单号 %s 不存在", trackingNum)
	}
	events := make([]map[string]string, len(info.Events))
	for i, e := range info.Events {
		events[i] = map[string]string{"time": e.Time, "event": e.Event, "station": e.Station}
	}
	return map[string]any{
		"tracking_number": info.TrackingNum, "status": info.Status,
		"location": info.Location, "estimated_arrival": info.EstArrival,
		"events": events,
	}, nil
}

type RecommendProductTool struct {
	tool.BaseTool
}

func NewRecommendProductTool() *RecommendProductTool {
	return &RecommendProductTool{
		BaseTool: tool.NewBaseTool(
			"recommend_product",
			"根据关键词搜索推荐商品。",
			[]model.ToolParameter{
				{Name: "keyword", Type: "string", Description: "搜索关键词", Required: true},
				{Name: "max_price", Type: "number", Description: "最高价格（可选）"},
			},
		),
	}
}

func (t *RecommendProductTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	keyword, _ := args["keyword"].(string)
	maxPrice, _ := args["max_price"].(float64)

	keyword = strings.ToLower(keyword)
	var results []map[string]any
	for _, p := range MockProducts {
		if !strings.Contains(strings.ToLower(p.Name), keyword) &&
			!strings.Contains(strings.ToLower(p.Category), keyword) &&
			!strings.Contains(strings.ToLower(p.Description), keyword) {
			continue
		}
		if maxPrice > 0 && p.Price > maxPrice {
			continue
		}
		results = append(results, map[string]any{
			"product_id": p.ProductID, "name": p.Name,
			"category": p.Category, "price": p.Price,
			"stock": p.Stock, "rating": p.Rating,
			"description": p.Description,
		})
	}
	if results == nil {
		return map[string]any{"message": "未找到匹配的商品", "results": []any{}}, nil
	}
	return map[string]any{"results": results}, nil
}

type CreateRefundTool struct {
	tool.BaseTool
}

func NewCreateRefundTool() *CreateRefundTool {
	return &CreateRefundTool{
		BaseTool: tool.NewBaseTool(
			"create_refund",
			"创建退款/退货申请。",
			[]model.ToolParameter{
				{Name: "order_id", Type: "string", Description: "订单号", Required: true},
				{Name: "user_id", Type: "string", Description: "用户ID", Required: true},
				{Name: "reason", Type: "string", Description: "退款原因", Required: true},
				{Name: "amount", Type: "number", Description: "退款金额", Required: true},
			},
		),
	}
}

func (t *CreateRefundTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	orderID, _ := args["order_id"].(string)
	userID, _ := args["user_id"].(string)
	reason, _ := args["reason"].(string)
	amount, _ := args["amount"].(float64)

	order, ok := MockOrders[orderID]
	if !ok {
		return nil, fmt.Errorf("订单 %s 不存在", orderID)
	}
	if order.UserID != userID {
		return nil, fmt.Errorf("该订单不属于用户 %s", userID)
	}
	if order.Status != OrderDelivered && order.Status != OrderCompleted {
		return nil, fmt.Errorf("订单状态为 %s，不可申请退款", order.Status)
	}

	reqID := fmt.Sprintf("ref%s", orderID)
	return map[string]any{
		"request_id": reqID, "order_id": orderID,
		"amount": amount, "status": "pending", "reason": reason,
		"message": fmt.Sprintf("退款申请已提交，金额 ¥%.2f，原因：%s，等待审核", amount, reason),
	}, nil
}

type TransferHumanTool struct {
	tool.BaseTool
}

func NewTransferHumanTool() *TransferHumanTool {
	return &TransferHumanTool{
		BaseTool: tool.NewBaseTool(
			"transfer_human",
			"转接人工客服。当无法处理用户问题或用户要求转人工时调用。",
			[]model.ToolParameter{
				{Name: "reason", Type: "string", Description: "转人工原因", Required: true},
			},
		),
	}
}

func (t *TransferHumanTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	reason, _ := args["reason"].(string)
	return map[string]any{
		"success": true,
		"message": fmt.Sprintf("已转接人工客服。转接原因：%s。请稍候，人工客服即将接入。", reason),
		"queue":   "您前面还有 2 位用户在等待",
	}, nil
}

type SendCouponTool struct {
	tool.BaseTool
}

func NewSendCouponTool() *SendCouponTool {
	return &SendCouponTool{
		BaseTool: tool.NewBaseTool(
			"send_coupon",
			"向用户发送优惠券作为补偿或安抚。",
			[]model.ToolParameter{
				{Name: "user_id", Type: "string", Description: "用户ID", Required: true},
				{Name: "amount", Type: "number", Description: "优惠券金额", Required: true},
				{Name: "reason", Type: "string", Description: "发放原因", Required: true},
			},
		),
	}
}

func (t *SendCouponTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	userID, _ := args["user_id"].(string)
	amount, _ := args["amount"].(float64)
	reason, _ := args["reason"].(string)

	if _, ok := MockUsers[userID]; !ok {
		return nil, fmt.Errorf("用户 %s 不存在", userID)
	}

	return map[string]any{
		"success":   true,
		"message":   fmt.Sprintf("已向用户 %s 发放 ¥%.0f 优惠券。原因：%s", userID, amount, reason),
		"coupon_id": fmt.Sprintf("cpn_%s_%.0f", userID, amount),
	}, nil
}
