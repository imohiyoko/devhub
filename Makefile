.PHONY: build install test vet lint fmt fmt-check new-tool

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

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

# Mirrors the CI gate: fails if anything is not gofmt-clean.
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

# Scaffold a new tool (Go generator; dash-in-id OK, registry auto-wired):
#   make new-tool NAME=notes
#   make new-tool NAME=my-tool ARGS=--page-only
new-tool:
	@test -n "$(NAME)" || { echo "usage: make new-tool NAME=<id> [ARGS=--page-only]"; exit 2; }
	@go run ./scripts/newtool "$(NAME)" $(ARGS)
