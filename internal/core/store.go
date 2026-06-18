package core

// Store is the narrow persistence seam a tool depends on. It is deliberately
// tiny: a tool sees only a key/value view, never the concrete *storage.Store.
// That keeps the door open to backing a tool with its own datastore once it is
// extracted to a service (decentralized data ownership).
type Store interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
}

// Namespace returns a Store view whose keys are transparently prefixed with the
// tool's ID. Hand each tool core.Namespace(deps.Store, meta.ID) so two tools
// can never collide on a key — and so "which tool owns this data" is structural,
// not a convention. This is the single highest-leverage microservices-ready
// move: enforce per-tool data ownership now, cheaply.
func Namespace(base Store, toolID string) Store {
	return nsStore{base: base, prefix: toolID + ":"}
}

type nsStore struct {
	base   Store
	prefix string
}

func (s nsStore) Get(key string) ([]byte, error)     { return s.base.Get(s.prefix + key) }
func (s nsStore) Set(key string, value []byte) error { return s.base.Set(s.prefix+key, value) }
