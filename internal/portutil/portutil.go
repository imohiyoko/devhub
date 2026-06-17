// Package portutil holds port-number helpers shared by the settings and ports
// controllers. Ports backend/controllers/ports.normalize_port_list.
package portutil

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// NormalizePortList coerces value (expected a JSON array) into a sorted, unique
// list of valid TCP ports (1-65535). In strict mode invalid input is an error;
// otherwise invalid entries are skipped. The result is always non-nil.
func NormalizePortList(value any, strict bool) ([]int, error) {
	list, ok := value.([]any)
	if !ok {
		if strict {
			return nil, fmt.Errorf("ports must be a list")
		}
		return []int{}, nil
	}
	seen := make(map[int]bool, len(list))
	ports := make([]int, 0, len(list))
	for _, item := range list {
		port, ok := coercePort(item)
		if !ok || port < 1 || port > 65535 {
			if strict {
				return nil, fmt.Errorf("ports must be integers from 1 to 65535")
			}
			continue
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}

// coercePort mirrors Python's int(str(item).strip()) with bools rejected.
func coercePort(item any) (int, bool) {
	switch v := item.(type) {
	case bool:
		return 0, false
	case float64:
		i := int(v)
		if float64(i) != v {
			return 0, false
		}
		return i, true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}
