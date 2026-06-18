.PHONY: build test vet fmt fmt-check new-tool

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Mirrors the CI gate: fails if anything is not gofmt-clean.
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

# Scaffold a new tool: make new-tool NAME=notes
new-tool:
	@test -n "$(NAME)" || { echo "usage: make new-tool NAME=<id>"; exit 2; }
	@scripts/new-tool.sh "$(NAME)"
