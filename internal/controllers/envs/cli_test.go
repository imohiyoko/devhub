package envs

import (
	"reflect"
	"testing"
)

func TestStopTargetPorts(t *testing.T) {
	envDef := map[string]any{
		"id": "dev",
		"processes": []any{
			map[string]any{"id": "api", "port": float64(3000)},
			map[string]any{"id": "web", "port": "8080-8082"},
			map[string]any{"id": "worker"},                // no port declared
			map[string]any{"id": "broken", "port": "abc"}, // invalid spec: skipped
			map[string]any{"id": "dup", "port": "3000"},   // duplicate of api
		},
	}
	launches := []any{
		// This env's offset launch: assigned_port wins over the recorded spec.
		map[string]any{"env_id": "dev", "processes": []any{
			map[string]any{"id": "api", "port": float64(3000), "assigned_port": float64(3001)},
			map[string]any{"id": "web", "port": "9000"}, // no assigned port: recorded spec counts
		}},
		// Another env's launch must not leak into dev's targets.
		map[string]any{"env_id": "other", "processes": []any{
			map[string]any{"id": "api", "assigned_port": float64(5000)},
		}},
		"not-a-record", // malformed entries are skipped
	}
	got := stopTargetPorts(decodeEnvironment(envDef), launches)
	want := []int{3000, 3001, 8080, 8081, 8082, 9000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stopTargetPorts = %v, want %v", got, want)
	}
}

func TestStopTargetPortsEmpty(t *testing.T) {
	envDef := map[string]any{"id": "dev", "processes": []any{map[string]any{"id": "a"}}}
	if got := stopTargetPorts(decodeEnvironment(envDef), nil); len(got) != 0 {
		t.Errorf("no declared ports should yield no targets, got %v", got)
	}
}
