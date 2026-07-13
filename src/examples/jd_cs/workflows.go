package jd_cs

import (
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

func updateIntentState(input, output string, state map[string]any) {
	InferStateFromOutput(output, state)
	intent, _ := classifyIntent(strings.ToLower(input))
	state[stateIntent] = string(intent)
}

func BuildCSWorkflow(triageAgent, orderAgent, refundAgent *engine.Agent) *orchestrator.Workflow {
	wf := orchestrator.NewWorkflow(orchestrator.WorkflowConfig{
		ID:   "jd-cs-workflow",
		Name: "京东智能客服工作流",
	})

	classifyStep := orchestrator.NewStepNode("classify", triageAgent, updateIntentState)

	handleOrder := orchestrator.NewStepNode("handle_order", orderAgent).WithInputFromState("user_input")
	handleRefund := orchestrator.NewStepNode("handle_refund", refundAgent).WithInputFromState("user_input")
	transferNode := orchestrator.NewStepNode("transfer_human", orderAgent).WithInputFromState("user_input")
	greetingNode := orchestrator.NewStepNode("greeting", orderAgent).WithInputFromState("user_input")

	nonRefundNode := orchestrator.NewConditionNode("dispatch_transfer",
		func(input string, state map[string]any) (bool, error) {
			intent, ok := state[stateIntent].(string)
			if !ok {
				return false, nil
			}
			return IntentType(intent) == IntentTransfer, nil
		},
		transferNode,
		orchestrator.NewConditionNode("dispatch_greeting",
			func(input string, state map[string]any) (bool, error) {
				intent, ok := state[stateIntent].(string)
				if !ok {
					return false, nil
				}
				return IntentType(intent) == IntentGreeting, nil
			},
			greetingNode,
			handleOrder,
		),
	)

	dispatchNode := orchestrator.NewConditionNode("dispatch_refund",
		func(input string, state map[string]any) (bool, error) {
			intent, ok := state[stateIntent].(string)
			if !ok {
				return false, nil
			}
			slog.Info("routing based on intent", "intent", intent)
			if IntentType(intent) == IntentRefund {
				state[stateNeedRefund] = true
				return true, nil
			}
			return false, nil
		},
		handleRefund,
		nonRefundNode,
	)

	wf.AddNode(classifyStep)
	wf.AddNode(dispatchNode)

	wf.SetState(stateIntent, string(IntentUnknown))
	wf.SetState(stateResult, "")

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

func InferStateFromOutput(output string, state map[string]any) {
	outputLower := strings.ToLower(output)

	if state[stateIntent] == nil || state[stateIntent] == string(IntentUnknown) {
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
