package main

import (
	"fmt"
	"os"

	devhub "github.com/imohiyoko/devhub"
	docspkg "github.com/imohiyoko/devhub/internal/docs"
	"github.com/imohiyoko/devhub/internal/jsonx"
)

const docsUsage = `Usage:
  devhub docs list           list the available documents (JSON: name + description)
  devhub docs show <name>    print one document

Documents are embedded in this binary, so they always match the devhub you are
running and need no checkout or network access. The same set is served over
HTTP at GET /ai-api/docs and GET /ai-api/docs/<name>.

Start with 'agent/ai-api' to drive devhub over HTTP, 'agent/cli' for the
command line, or 'agent/troubleshooting' when a request failed.
`

// docsHint is appended to help and version output, and to the errors an agent
// is most likely to hit first. A docs command nothing points at is a docs
// command nothing finds: agents routinely run a bare command or --version
// without reading help, so the pointer has to sit on more than one exit.
const docsHint = "Run `devhub docs list` to see the available documentation."

// runDocs executes `devhub docs <subcommand>` and returns the process exit code.
func runDocs(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(docsUsage)
		return 0
	}

	set, err := docspkg.Load(devhub.Docs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: docs:", err)
		return 1
	}

	switch args[0] {
	case "list":
		// JSON, not a table: this output is read by agents far more often than by
		// people, and the help field tells a first-time reader what to do with a
		// name it just found (the article's docs-list convention).
		b, err := jsonx.Marshal(map[string]any{
			"docs": set.List(),
			"help": "Run `devhub docs show <name>` to read one of these.",
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "devhub: docs:", err)
			return 1
		}
		fmt.Println(string(b))
		return 0

	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "devhub: docs show needs a document name (see `devhub docs list`)")
			return 2
		}
		body, err := set.Show(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "devhub:", err)
			return 1
		}
		fmt.Print(body)
		return 0

	default:
		fmt.Fprintf(os.Stderr, "devhub: unknown docs subcommand %q\n\n%s", args[0], docsUsage)
		return 2
	}
}
