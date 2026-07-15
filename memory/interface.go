package memory

import (
	"github.com/apexracing/tracklogic-agent/types"
)

type Memory interface {
	Add(msg types.Message)
	Get(index int) (types.Message, bool)
	Recent(n int) []types.Message
	Snapshot() []types.Message
	Clear()
	Len() int
}
