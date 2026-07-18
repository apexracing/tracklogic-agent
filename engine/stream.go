package engine

import (
	"context"
	"strings"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/types"
)

// consumeStream drains an InvokeStream channel into a complete InvokeResponse,
// forwarding non-empty content deltas via onChunk (may be nil).
func consumeStream(ctx context.Context, ch <-chan model.ResponseChunk, onChunk, onReasoning func(string)) (*model.InvokeResponse, error) {
	var (
		contentBuilder   strings.Builder
		reasoningBuilder strings.Builder
		toolCalls        []types.ToolCall
		toolIndex        = map[string]int{} // tool call ID -> index in toolCalls
		finishReason     string
		usage            *types.Usage
	)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				return &model.InvokeResponse{
					Content:      contentBuilder.String(),
					Reasoning:    reasoningBuilder.String(),
					ToolCalls:    toolCalls,
					Usage:        usage,
					FinishReason: finishReason,
				}, nil
			}
			if chunk.Error != nil {
				return nil, chunk.Error
			}
			if chunk.Content != "" {
				contentBuilder.WriteString(chunk.Content)
				if onChunk != nil {
					onChunk(chunk.Content)
				}
			}
			if chunk.Reasoning != "" {
				reasoningBuilder.WriteString(chunk.Reasoning)
				if onReasoning != nil {
					onReasoning(chunk.Reasoning)
				}
			}
			if chunk.ToolCall != nil {
				mergeToolCall(&toolCalls, toolIndex, chunk.ToolCall)
			}
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
			}
			if chunk.Done {
				return &model.InvokeResponse{
					Content:      contentBuilder.String(),
					Reasoning:    reasoningBuilder.String(),
					ToolCalls:    toolCalls,
					Usage:        usage,
					FinishReason: finishReason,
				}, nil
			}
		}
	}
}

// mergeToolCall appends or merges an incremental tool-call delta.
// Matching is by ID when present; otherwise the last incomplete call is extended
// (Chat Completions style argument fragments often omit ID after the first delta).
func mergeToolCall(calls *[]types.ToolCall, index map[string]int, delta *types.ToolCall) {
	if delta.ID != "" {
		if i, ok := index[delta.ID]; ok {
			mergeInto(&(*calls)[i], delta)
			return
		}
		*calls = append(*calls, *delta)
		index[delta.ID] = len(*calls) - 1
		return
	}

	// No ID: append args/name onto the last tool call if any, else start a new one.
	if len(*calls) == 0 {
		*calls = append(*calls, *delta)
		return
	}
	mergeInto(&(*calls)[len(*calls)-1], delta)
}

func mergeInto(dst *types.ToolCall, src *types.ToolCall) {
	if src.ID != "" {
		dst.ID = src.ID
	}
	if src.Type != "" {
		dst.Type = src.Type
	}
	if src.Function.Name != "" {
		dst.Function.Name = src.Function.Name
	}
	if src.Function.Arguments != "" {
		dst.Function.Arguments += src.Function.Arguments
	}
}
