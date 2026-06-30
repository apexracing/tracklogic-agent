package memory

import (
	"go-harness-tutorial/pkg/types"
)

type Memory interface {
	Add(msg types.Message)
	Get(index int) (types.Message, bool)
	Recent(n int) []types.Message
	Snapshot() []types.Message
	Clear()
	Len() int
}
