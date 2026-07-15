package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/apexracing/tracklogic-agent/types"
)

var fallbackRunID atomic.Uint64

func ensureRunContext(ctx context.Context, agentID string) (context.Context, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if current, ok := types.RunContextFrom(ctx); ok {
		copy := *current
		if copy.RunID == "" {
			copy.RunID = newRunID()
		}
		if copy.AgentID == "" {
			copy.AgentID = agentID
		}
		return types.WithRunContext(ctx, &copy), copy.RunID
	}

	runID := newRunID()
	runContext := &types.RunContext{RunID: runID, AgentID: agentID}
	return types.WithRunContext(ctx, runContext), runID
}

func newRunID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "run_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("run_%d_%d", time.Now().UnixNano(), fallbackRunID.Add(1))
}
