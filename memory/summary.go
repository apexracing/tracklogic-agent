package memory

import (
	"sync"

	"github.com/apexracing/tracklogic-agent/types"
)

// SummaryMemory retains raw messages until the engine supplies a successful
// summary. It never silently discards history at its hard limit.
type SummaryMemory struct {
	mu         sync.RWMutex
	messages   []types.Message
	summary    string
	keepRecent int
}

func NewSummaryMemory(keepRecent int) *SummaryMemory {
	if keepRecent <= 0 {
		keepRecent = 20
	}
	return &SummaryMemory{messages: make([]types.Message, 0, keepRecent*2), keepRecent: keepRecent}
}

func (m *SummaryMemory) Add(message types.Message) {
	m.mu.Lock()
	m.messages = append(m.messages, message)
	m.mu.Unlock()
}

func (m *SummaryMemory) Get(index int) (types.Message, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if index < 0 || index >= len(m.messages) {
		return types.Message{}, false
	}
	return m.messages[index], true
}

func (m *SummaryMemory) Recent(count int) []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if count <= 0 {
		return nil
	}
	if count > len(m.messages) {
		count = len(m.messages)
	}
	result := make([]types.Message, count)
	copy(result, m.messages[len(m.messages)-count:])
	return result
}

func (m *SummaryMemory) Snapshot() []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]types.Message, len(m.messages))
	copy(result, m.messages)
	return result
}

func (m *SummaryMemory) Clear() {
	m.mu.Lock()
	m.messages = nil
	m.summary = ""
	m.mu.Unlock()
}

func (m *SummaryMemory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

func (m *SummaryMemory) Summary() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.summary
}

// Compact installs a caller-produced summary only after it has succeeded and
// retains the configured recent raw messages.
func (m *SummaryMemory) Compact(summary string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := m.keepRecent
	if keep > len(m.messages) {
		keep = len(m.messages)
	}
	recent := make([]types.Message, keep)
	copy(recent, m.messages[len(m.messages)-keep:])
	m.summary = summary
	m.messages = recent
}

func (m *SummaryMemory) Restore(summary string, messages []types.Message) {
	m.mu.Lock()
	m.summary = summary
	m.messages = append([]types.Message(nil), messages...)
	m.mu.Unlock()
}
