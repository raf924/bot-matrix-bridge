package utils

import "sync"

type SyncMap[K any, V any] struct {
	m sync.Map
}

func NewTypedMap[K any, V any]() *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m: sync.Map{},
	}
}

func (m *SyncMap[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

func (m *SyncMap[K, V]) Load(key K) (V, bool) {
	v, ok := m.m.Load(key)
	if !ok {
		return *new(V), false
	}
	return v.(V), ok
}

func (m *SyncMap[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}

func (m *SyncMap[K, V]) Delete(key K) {
	m.m.Delete(key)
}
