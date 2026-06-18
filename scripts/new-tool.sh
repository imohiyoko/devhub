#!/usr/bin/env bash
# Scaffold a new devhub tool: a core.Tool adapter and a page stub.
#
#   scripts/new-tool.sh <id>
#
# <id> is the tool's route namespace: the page is served at /<id> and the card
# links there. Use a Go-identifier-safe id (lowercase letters/digits, no dashes)
# so the generated type name is valid; set a nicer display name in Title.
set -euo pipefail

id="${1:-}"
if [ -z "$id" ]; then
  echo "usage: scripts/new-tool.sh <id>   (e.g. notes)" >&2
  exit 2
fi
if ! printf '%s' "$id" | grep -qE '^[a-z][a-z0-9]*$'; then
  echo "::error:: id must match ^[a-z][a-z0-9]*$ (got '$id'). Dashes break the Go type name." >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
go_file="$root/internal/tools/$id.go"
page_dir="$root/tools/$id"
page_file="$page_dir/index.html"

if [ -e "$go_file" ] || [ -e "$page_file" ]; then
  echo "::error:: $id already exists ($go_file or $page_file)" >&2
  exit 1
fi

cap="$(printf '%s' "$id" | cut -c1 | tr '[:lower:]' '[:upper:]')$(printf '%s' "$id" | cut -c2-)"

cat > "$go_file" <<EOF
package tools

import (
	"net/http"

	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// ${id}Tool is the $id tool. Replace this stub with real behavior.
type ${id}Tool struct{}

func new${cap}() core.Tool { return ${id}Tool{} }

func (t ${id}Tool) Meta() core.Meta {
	return core.Meta{
		ID:    "$id",
		Title: "$id",
		Icon:  "🔧",
		Desc:  "TODO: describe $id",
		Page:  "tools/$id/index.html",
	}
}

func (t ${id}Tool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/$id/ping", Handle: func(w http.ResponseWriter, _ *http.Request) error {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
			return nil
		}},
	}
}
EOF

mkdir -p "$page_dir"
cat > "$page_file" <<EOF
<!doctype html>
<html lang="ja">
<head><meta charset="utf-8"><title>$id</title></head>
<body>
  <h1>$id</h1>
  <p>TODO: build the $id UI. The API token shim is injected automatically.</p>
  <pre id="out">loading…</pre>
  <script>
    fetch('/api/$id/ping').then(r => r.json()).then(d => {
      document.getElementById('out').textContent = JSON.stringify(d);
    });
  </script>
</body>
</html>
EOF

echo "created:"
echo "  $go_file"
echo "  $page_file"
echo
echo "next: add this line to internal/tools/registry.go's NewRegistry(...) list:"
echo "      new${cap}(),"
echo
echo "then: go build ./... && go run ./cmd/devhub   # the card appears automatically"
