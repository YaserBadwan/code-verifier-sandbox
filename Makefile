GOBIN_DIR := $(shell go env GOPATH)/bin
PROTO     := proto/verifier/v1/verifier.proto

.DEFAULT_GOAL := help
.PHONY: help build run images test test-integration lint proto example clean

help: ## show this list
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  make %-18s %s\n", $$1, $$2}'

build: ## build bin/verifier and bin/verifierctl
	go build -o bin/verifier ./cmd/verifier
	go build -o bin/verifierctl ./cmd/verifierctl

run: build ## build, start the server and open the web interface
	./bin/verifier -open

images: ## download the Docker images for the enabled languages (VERIFIER_LANGUAGES)
	go run ./cmd/verifier -pull-only

test: ## unit tests, no Docker needed
	go test -race ./...

# -p 1 because the cancel test checks that no sandbox resources are left behind
test-integration: ## full test suite against the real Docker daemon
	go test -race -tags integration -p 1 -count=1 -timeout 20m ./...

lint: ## gofmt and go vet
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w on the files above" && exit 1)
	go vet ./...
	go vet -tags integration ./...

proto: ## regenerate the Go code from the .proto file (needs protoc)
	PATH="$(GOBIN_DIR):$$PATH" protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO)

example: build ## verify the Python example against a running server
	./bin/verifierctl run -file examples/sum/solution.py -tests examples/sum/tests.json

clean: ## remove build output
	rm -rf bin
