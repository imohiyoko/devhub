package storage

import "encoding/json"

const activeInstanceKey = "runtime.active_instance"

// ActiveInstance identifies the process which most recently bound successfully
// for this DEVHUB_HOME. CLI stop uses the port only as a probe candidate; the
// nonce-bound HMAC and live-listener PID check remain the authority.
type ActiveInstance struct {
	Port     int    `json:"port"`
	PID      int    `json:"pid"`
	Instance string `json:"instance"`
}

func (s *Store) RecordActiveInstance(instance ActiveInstance) error {
	b, err := json.Marshal(instance)
	if err != nil {
		return err
	}
	return s.Set(activeInstanceKey, b)
}

func (s *Store) LoadActiveInstance() (ActiveInstance, error) {
	var instance ActiveInstance
	b, err := s.Get(activeInstanceKey)
	if err != nil || b == nil {
		return instance, err
	}
	if err := json.Unmarshal(b, &instance); err != nil {
		return ActiveInstance{}, err
	}
	return instance, nil
}

// ClearActiveInstance removes instance only when it is still the current
// record. The value comparison prevents an exiting old process from erasing a
// replacement process's newer record.
func (s *Store) ClearActiveInstance(instance ActiveInstance) error {
	b, err := json.Marshal(instance)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM kv WHERE key = ? AND value = ?`, activeInstanceKey, string(b))
	return err
}
