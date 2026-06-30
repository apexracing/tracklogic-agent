package memory

import (
	"go-harness-tutorial/pkg/types"
)

type BufferMemory struct {
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
	if len(m.messages) >= m.capacity {
		keep := m.messages[m.capacity/2:]
		m.messages = keep
	}
	m.messages = append(m.messages, msg)
}

func (m *BufferMemory) Get(index int) (types.Message, bool) {
	if index < 0 || index >= len(m.messages) {
		return types.Message{}, false
	}
	return m.messages[index], true
}

func (m *BufferMemory) Recent(n int) []types.Message {
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
	result := make([]types.Message, len(m.messages))
	copy(result, m.messages)
	return result
}

func (m *BufferMemory) Clear() {
	m.messages = make([]types.Message, 0, m.capacity)
}

func (m *BufferMemory) Len() int {
	return len(m.messages)
}
