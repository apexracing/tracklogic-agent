package jd_cs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go-harness-tutorial/internal/engine"
	"go-harness-tutorial/internal/orchestrator"
)

const (
	stateIntent     = "intent"
	stateUserID     = "user_id"
	stateOrderID    = "order_id"
	stateNeedRefund = "need_refund"
	stateResult     = "result"
)

func BuildCSWorkflow(triageAgent, orderAgent, refundAgent *engine.Agent) *orchestrator.Workflow {
	wf := orchestrator.NewWorkflow(orchestrator.WorkflowConfig{
		ID:   "jd-cs-workflow",
		Name: "京东智能客服工作流",
	})

	classifyStep := orchestrator.NewStepNode("classify", triageAgent)

	handleOrder := orchestrator.NewStepNode("handle_order", orderAgent)

	handleRefund := orchestrator.NewStepNode("handle_refund", refundAgent)

	transferNode := orchestrator.NewStepNode("transfer_human", orderAgent)

	routeNode := orchestrator.NewConditionNode("route",
		func(input string, state map[string]any) (bool, error) {
			intent, ok := state[stateIntent].(string)
			if !ok {
				return false, nil
			}
			slog.Info("routing based on intent", "intent", intent)
			switch IntentType(intent) {
			case IntentRefund:
				state[stateNeedRefund] = true
				return true, nil
			case IntentTransfer:
				return false, nil
			default:
				return false, nil
			}
		},
		handleRefund,
		transferNode,
	)

	greetingNode := orchestrator.NewStepNode("greeting", orderAgent)

	intentGate := orchestrator.NewConditionNode("intent_gate",
		func(input string, state map[string]any) (bool, error) {
			intent, ok := state[stateIntent].(string)
			if !ok {
				return false, nil
			}
			return IntentType(intent) != IntentGreeting, nil
		},
		routeNode,
		greetingNode,
	)

	wf.AddNode(classifyStep)
	wf.AddNode(intentGate)
	wf.AddNode(handleOrder)

	wf.SetState(stateIntent, IntentUnknown)
	wf.SetState(stateResult, "")

	return wf
}

func BuildSimpleCSWorkflow(triageAgent *engine.Agent) *orchestrator.Workflow {
	wf := orchestrator.NewWorkflow(orchestrator.WorkflowConfig{
		ID:   "jd-cs-simple",
		Name: "京东客服简易分流工作流",
	})

	classifyStep := orchestrator.NewStepNode("classify", triageAgent)

	loopNode := orchestrator.NewLoopNode("follow_up",
		orchestrator.NewStepNode("respond", triageAgent),
		func(iteration int, input string, state map[string]any) (bool, error) {
			return iteration < 1, nil
		},
		3,
	)

	wf.AddNode(classifyStep)
	wf.AddNode(loopNode)

	return wf
}

func BuildAfterSalesWorkflow(orderAgent, refundAgent *engine.Agent) *orchestrator.Workflow {
	wf := orchestrator.NewWorkflow(orchestrator.WorkflowConfig{
		ID:   "after-sales",
		Name: "售后处理工作流",
	})

	verifyOrder := orchestrator.NewStepNode("verify_order", orderAgent)

	processRefund := orchestrator.NewStepNode("process_refund", refundAgent)

	compensationNode := orchestrator.NewStepNode("compensation", orderAgent)

	needsCompensation := orchestrator.NewConditionNode("needs_compensation",
		func(input string, state map[string]any) (bool, error) {
			comp, ok := state["needs_compensation"]
			if !ok {
				return false, nil
			}
			return fmt.Sprintf("%v", comp) == "true", nil
		},
		compensationNode,
		orchestrator.NewStepNode("done", orderAgent),
	)

	wf.AddNode(verifyOrder)
	wf.AddNode(processRefund)
	wf.AddNode(needsCompensation)

	return wf
}

func BuildTeamCS(h *Harness) error {
	team := h.NewTeam(orchestrator.TeamConfig{
		ID:   "jd-cs-team",
		Name: "jd_cs_team",
		Mode: orchestrator.ModeSequential,
	})
	_ = team
	return nil
}

func InferStateFromOutput(output string, state map[string]any) {
	outputLower := strings.ToLower(output)

	if state[stateIntent] == nil || state[stateIntent] == IntentUnknown {
		if strings.Contains(outputLower, "query_order") || strings.Contains(outputLower, "订单") {
			state[stateIntent] = string(IntentQueryOrder)
		} else if strings.Contains(outputLower, "refund") || strings.Contains(outputLower, "退款") {
			state[stateIntent] = string(IntentRefund)
		} else if strings.Contains(outputLower, "transfer") || strings.Contains(outputLower, "转人工") {
			state[stateIntent] = string(IntentTransfer)
		} else if strings.Contains(outputLower, "greeting") {
			state[stateIntent] = string(IntentGreeting)
		}
	}
}
