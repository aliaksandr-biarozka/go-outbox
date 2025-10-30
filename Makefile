.PHONY: test test-unit test-integration test-all deps clean

# Detect Docker socket (supports Colima, Docker Desktop, and standard Docker)
DOCKER_SOCKET := $(shell if [ -S "${HOME}/.colima/default/docker.sock" ]; then echo "unix://${HOME}/.colima/default/docker.sock"; elif [ -S "${HOME}/.docker/run/docker.sock" ]; then echo "unix://${HOME}/.docker/run/docker.sock"; else echo "unix:///var/run/docker.sock"; fi)

# Run all tests
test-all: test

# Run only unit tests
test-unit:
	go test -v -short ./...

# Run only integration tests
test-integration:
	DOCKER_HOST=$(DOCKER_SOCKET) TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock go test -v -run "TestIntegration" ./...

# Run all tests (unit + integration)
test:
	DOCKER_HOST=$(DOCKER_SOCKET) TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock go test -v ./...

# Run tests with coverage
test-coverage:
	DOCKER_HOST=$(DOCKER_SOCKET) TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Install dependencies
deps:
	go mod download
	go mod tidy

# Clean up
clean:
	go clean
	rm -f coverage.out coverage.html

# Help
help:
	@echo "Available targets:"
	@echo "  test-unit          - Run only unit tests"
	@echo "  test-integration   - Run integration tests (requires Docker)"
	@echo "  test               - Run all tests"
	@echo "  test-coverage      - Run tests with coverage report"
	@echo "  deps               - Install dependencies"
	@echo "  clean              - Clean up generated files"
	@echo "  help               - Show this help message"
