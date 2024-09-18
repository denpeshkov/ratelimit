.PHONY: all
all: tidy lint test/cover

.PHONY: help
help: ## Display this help screen
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: tidy
tidy: ## Tidy
	go mod tidy -v
	go mod verify
	go fmt ./... 
	go vet ./...
	staticcheck ./...

.PHONY: lint
lint: ## Lint
	docker run -t --rm -v .:/app -v ~/.cache/golangci-lint/v1.61.0:/root/.cache -w /app golangci/golangci-lint:v1.61.0 golangci-lint run -v -c .golangci.yml

.PHONY: test
test: ## Test
	go test -race -buildvcs -count=1 ./...

.PHONY: test/cover
test/cover: ## Test and cover
	go test -race -count=1 -coverprofile=cover.out ./...
	go tool cover -html=cover.out
