.PHONY: build install test vet fmt fmt-check new-tool

build:
	go build ./...

# Build from the current source and update the `devhub` command on PATH
# (no release needed). See scripts/dev.sh install.
install:
	@scripts/dev.sh install

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
