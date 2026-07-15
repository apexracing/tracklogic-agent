package memory

import (
	"sync"

	"github.com/apexracing/tracklogic-agent/types"
)

type BufferMemory struct {
	mu       sync.RWMutex
	messages []types.Message
	capacity int
}

func NewBufferMemory(capacity int) *BufferMemory {
	if capacity <= 0 {
		capacity = 100
	}
	return &BufferMemory{
		messages: make([]types.Message, 0, capacity),
		capacity: capacity,
	}
}

func (m *BufferMemory) Add(msg types.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.messages) >= m.capacity {
		cut := m.capacity / 2
		if cut < 1 {
			cut = 1
		}
		start := 0
		for start < len(m.messages) && m.messages[start].Role == types.RoleSystem {
			start++
		}
		if start < cut {
			m.messages = append(m.messages[:start], m.messages[cut:]...)
		} else {
			m.messages = m.messages[cut:]
		}
	}
	m.messages = append(m.messages, msg)
}

func (m *BufferMemory) Get(index int) (types.Message, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if index < 0 || index >= len(m.messages) {
		return types.Message{}, false
	}
	return m.messages[index], true
}

func (m *BufferMemory) Recent(n int) []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if n <= 0 {
		return nil
	}
	if n >= len(m.messages) {
		result := make([]types.Message, len(m.messages))
		copy(result, m.messages)
		return result
	}
	result := make([]types.Message, n)
	copy(result, m.messages[len(m.messages)-n:])
	return result
}

func (m *BufferMemory) Snapshot() []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]types.Message, len(m.messages))
	copy(result, m.messages)
	return result
}

func (m *BufferMemory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = make([]types.Message, 0, m.capacity)
}

func (m *BufferMemory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}
