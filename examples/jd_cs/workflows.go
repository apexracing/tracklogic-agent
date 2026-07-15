package jd_cs

import (
	"fmt"
	"log/slog"
	"strings"

	agent "github.com/apexracing/tracklogic-agent"
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

func BuildCSWorkflow(triageAgent, orderAgent, refundAgent *agent.Agent) *agent.Workflow {
	wf := agent.NewWorkflow(agent.WorkflowConfig{
		ID:   "jd-cs-workflow",
		Name: "京东智能客服工作流",
	})

	classifyStep := agent.NewStepNode("classify", triageAgent, updateIntentState)

	handleOrder := agent.NewStepNode("handle_order", orderAgent).WithInputFromState("user_input")
	handleRefund := agent.NewStepNode("handle_refund", refundAgent).WithInputFromState("user_input")
	transferNode := agent.NewStepNode("transfer_human", orderAgent).WithInputFromState("user_input")
	greetingNode := agent.NewStepNode("greeting", orderAgent).WithInputFromState("user_input")

	nonRefundNode := agent.NewConditionNode("dispatch_transfer",
		func(input string, state map[string]any) (bool, error) {
			intent, ok := state[stateIntent].(string)
			if !ok {
				return false, nil
			}
			return IntentType(intent) == IntentTransfer, nil
		},
		transferNode,
		agent.NewConditionNode("dispatch_greeting",
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

	dispatchNode := agent.NewConditionNode("dispatch_refund",
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

func BuildAfterSalesWorkflow(orderAgent, refundAgent *agent.Agent) *agent.Workflow {
	wf := agent.NewWorkflow(agent.WorkflowConfig{
		ID:   "after-sales",
		Name: "售后处理工作流",
	})

	verifyOrder := agent.NewStepNode("verify_order", orderAgent)
	processRefund := agent.NewStepNode("process_refund", refundAgent)
	compensationNode := agent.NewStepNode("compensation", orderAgent)

	needsCompensation := agent.NewConditionNode("needs_compensation",
		func(input string, state map[string]any) (bool, error) {
			comp, ok := state["needs_compensation"]
			if !ok {
				return false, nil
			}
			return fmt.Sprintf("%v", comp) == "true", nil
		},
		compensationNode,
		agent.NewStepNode("done", orderAgent),
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
