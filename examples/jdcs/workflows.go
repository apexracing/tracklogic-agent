package jdcs

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/workflow"
)

const (
	stateIntent     = "intent"
	stateUserID     = "user_id"
	stateOrderID    = "order_id"
	stateNeedRefund = "need_refund"
	stateResult     = "result"
)

func updateIntentState(input, output string, state *workflow.State) {
	InferStateFromOutput(output, state)
	intent, _ := classifyIntent(strings.ToLower(input))
	state.Set(stateIntent, string(intent))
}

func BuildCSWorkflow(triageAgent, orderAgent, refundAgent *engine.Agent) *workflow.Workflow {
	wf := workflow.NewWorkflow(workflow.WorkflowConfig{
		ID:   "jd-cs-workflow",
		Name: "京东智能客服工作流",
	})

	classifyStep := workflow.NewStepNode("classify", triageAgent, updateIntentState)

	handleOrder := workflow.NewStepNode("handle_order", orderAgent).WithInputFromState("user_input")
	handleRefund := workflow.NewStepNode("handle_refund", refundAgent).WithInputFromState("user_input")
	transferNode := workflow.NewStepNode("transfer_human", orderAgent).WithInputFromState("user_input")
	greetingNode := workflow.NewStepNode("greeting", orderAgent).WithInputFromState("user_input")

	nonRefundNode := workflow.NewConditionNode("dispatch_transfer",
		func(input string, state *workflow.State) (bool, error) {
			value, exists := state.Get(stateIntent)
			intent, ok := value.(string)
			if !exists || !ok {
				return false, nil
			}
			return IntentType(intent) == IntentTransfer, nil
		},
		transferNode,
		workflow.NewConditionNode("dispatch_greeting",
			func(input string, state *workflow.State) (bool, error) {
				value, exists := state.Get(stateIntent)
				intent, ok := value.(string)
				if !exists || !ok {
					return false, nil
				}
				return IntentType(intent) == IntentGreeting, nil
			},
			greetingNode,
			handleOrder,
		),
	)

	dispatchNode := workflow.NewConditionNode("dispatch_refund",
		func(input string, state *workflow.State) (bool, error) {
			value, exists := state.Get(stateIntent)
			intent, ok := value.(string)
			if !exists || !ok {
				return false, nil
			}
			slog.Info("routing based on intent", "intent", intent)
			if IntentType(intent) == IntentRefund {
				state.Set(stateNeedRefund, true)
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

func BuildAfterSalesWorkflow(orderAgent, refundAgent *engine.Agent) *workflow.Workflow {
	wf := workflow.NewWorkflow(workflow.WorkflowConfig{
		ID:   "after-sales",
		Name: "售后处理工作流",
	})

	verifyOrder := workflow.NewStepNode("verify_order", orderAgent)
	processRefund := workflow.NewStepNode("process_refund", refundAgent)
	compensationNode := workflow.NewStepNode("compensation", orderAgent)

	needsCompensation := workflow.NewConditionNode("needs_compensation",
		func(input string, state *workflow.State) (bool, error) {
			comp, ok := state.Get("needs_compensation")
			if !ok {
				return false, nil
			}
			return fmt.Sprintf("%v", comp) == "true", nil
		},
		compensationNode,
		workflow.NewStepNode("done", orderAgent),
	)

	wf.AddNode(verifyOrder)
	wf.AddNode(processRefund)
	wf.AddNode(needsCompensation)

	return wf
}

func InferStateFromOutput(output string, state *workflow.State) {
	outputLower := strings.ToLower(output)

	intentValue, _ := state.Get(stateIntent)
	if intentValue == nil || intentValue == string(IntentUnknown) {
		if strings.Contains(outputLower, "query_order") || strings.Contains(outputLower, "订单") {
			state.Set(stateIntent, string(IntentQueryOrder))
		} else if strings.Contains(outputLower, "refund") || strings.Contains(outputLower, "退款") {
			state.Set(stateIntent, string(IntentRefund))
		} else if strings.Contains(outputLower, "transfer") || strings.Contains(outputLower, "转人工") {
			state.Set(stateIntent, string(IntentTransfer))
		} else if strings.Contains(outputLower, "greeting") {
			state.Set(stateIntent, string(IntentGreeting))
		}
	}
}
