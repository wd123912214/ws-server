package whitelist

import (
	"errors"
	"sort"
	"sync"

	"ws-server/internal/protocol"
)

var ErrInvalidUID = errors.New("invalid whitelist UID")

type Persister func([]string) error

type Manager struct {
	mu        sync.RWMutex
	replaceMu sync.Mutex
	allowed   map[string]struct{}
	persist   Persister
}

func New(initial []string, persist Persister) (*Manager, error) {
	normalized, err := normalize(initial)
	if err != nil {
		return nil, err
	}
	return &Manager{allowed: makeSet(normalized), persist: persist}, nil
}

func (m *Manager) Allowed(uid string) bool {
	m.mu.RLock()
	_, ok := m.allowed[uid]
	m.mu.RUnlock()
	return ok
}

func (m *Manager) List() []string {
	m.mu.RLock()
	result := make([]string, 0, len(m.allowed))
	for uid := range m.allowed {
		result = append(result, uid)
	}
	m.mu.RUnlock()
	sort.Strings(result)
	return result
}

func (m *Manager) Replace(uids []string) ([]string, error) {
	m.replaceMu.Lock()
	defer m.replaceMu.Unlock()
	normalized, err := normalize(uids)
	if err != nil {
		return nil, err
	}
	if m.persist != nil {
		if err := m.persist(append([]string(nil), normalized...)); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	m.allowed = makeSet(normalized)
	m.mu.Unlock()
	return append([]string(nil), normalized...), nil
}

func normalize(uids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(uids))
	for _, uid := range uids {
		if !protocol.ValidUID(uid) {
			return nil, ErrInvalidUID
		}
		seen[uid] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for uid := range seen {
		result = append(result, uid)
	}
	sort.Strings(result)
	return result, nil
}

func makeSet(uids []string) map[string]struct{} {
	result := make(map[string]struct{}, len(uids))
	for _, uid := range uids {
		result[uid] = struct{}{}
	}
	return result
}
