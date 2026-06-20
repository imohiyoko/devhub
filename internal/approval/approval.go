package approval

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/imohiyoko/devhub/internal/storage"
)

type Decision string

const (
	Approved Decision = "approved"
	Rejected Decision = "rejected"
	Pending  Decision = "pending"
)

type Rule struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	DetailPattern string `json:"detail_pattern"`
}

type Request struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"` // e.g. "execute_command", "write_file"
	Detail    string    `json:"detail"` // e.g. Description of what is being run or edited
	Status    Decision  `json:"status"`
	CreatedAt time.Time `json:"created_at"`

	done chan Decision
}

type Manager struct {
	mu       sync.RWMutex
	requests map[string]*Request
	rules    []Rule
	store    *storage.Store
}

func NewManager(store *storage.Store) *Manager {
	m := &Manager{
		requests: make(map[string]*Request),
		store:    store,
	}
	m.loadRules()
	return m
}

func (m *Manager) loadRules() {
	if m.store == nil {
		return
	}
	raw, err := m.store.LoadAIRules()
	if err != nil || raw == nil {
		return
	}
	var rules []Rule
	if err := json.Unmarshal(raw, &rules); err == nil {
		m.rules = rules
	}
}

func (m *Manager) saveRules() {
	if m.store == nil {
		return
	}
	b, err := json.Marshal(m.rules)
	if err != nil {
		return
	}
	_ = m.store.SaveAIRules(b)
}

func (m *Manager) Register(action, detail string) *Request {
	m.mu.Lock()
	defer m.mu.Unlock()

	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)

	req := &Request{
		ID:        id,
		Action:    action,
		Detail:    detail,
		Status:    Pending,
		CreatedAt: time.Now(),
		done:      make(chan Decision, 1),
	}
	m.requests[id] = req
	return req
}

func (m *Manager) ShouldAutoApprove(action, detail string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, rule := range m.rules {
		if rule.Action == action && strings.HasPrefix(detail, rule.DetailPattern) {
			return true
		}
	}
	return false
}

func (m *Manager) AddAlwaysAllowRule(action, detailPattern string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)

	rule := Rule{
		ID:            id,
		Action:        action,
		DetailPattern: detailPattern,
	}
	m.rules = append(m.rules, rule)
	m.saveRules()
	return id
}

func (m *Manager) ListRules() []Rule {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]Rule, len(m.rules))
	copy(res, m.rules)
	return res
}

func (m *Manager) DeleteRule(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, rule := range m.rules {
		if rule.ID == id {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			m.saveRules()
			return nil
		}
	}
	return errors.New("rule not found")
}

func (m *Manager) Wait(req *Request, timeout time.Duration) (Decision, error) {
	select {
	case d := <-req.done:
		return d, nil
	case <-time.After(timeout):
		m.mu.Lock()
		defer m.mu.Unlock()
		if req.Status == Pending {
			req.Status = Rejected
			select {
			case req.done <- Rejected:
			default:
			}
		}
		return Rejected, errors.New("timeout waiting for approval")
	}
}

func (m *Manager) ListPending() []*Request {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := []*Request{}
	for _, req := range m.requests {
		if req.Status == Pending {
			list = append(list, req)
		}
	}
	return list
}

func (m *Manager) Respond(id string, d Decision) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	req, ok := m.requests[id]
	if !ok {
		return errors.New("request not found")
	}
	if req.Status != Pending {
		return errors.New("request already resolved")
	}

	req.Status = d
	select {
	case req.done <- d:
	default:
	}
	return nil
}
