BINARY := comms-server
VERSION := 0.1.0
MODULE := github.com/Parsons-ai/comms-server

.PHONY: build test verify clean run cross-compile lint

## build: Build the server binary
build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/server

## test: Run all tests
test:
	go test -v -race -count=1 ./...

## verify: Full build verification — compile, test, start server, hit health endpoint
verify: build test
	@echo "=== Starting server for health check ==="
	@./$(BINARY) --data-dir /tmp/comms-verify-$$$$ --api-port 19080 --ws-port 19443 &
	@SERVER_PID=$$!; \
	sleep 2; \
	echo "=== Checking API health ==="; \
	curl -sf http://localhost:19080/api/v1/health && echo " OK" || echo " FAIL"; \
	echo "=== Checking signaling health ==="; \
	curl -sf http://localhost:19443/health && echo " OK" || echo " FAIL"; \
	echo "=== Checking identity endpoint ==="; \
	curl -sf http://localhost:19080/api/v1/identity && echo " OK" || echo " FAIL"; \
	echo "=== Stopping server ==="; \
	kill $$SERVER_PID 2>/dev/null; \
	wait $$SERVER_PID 2>/dev/null; \
	rm -rf /tmp/comms-verify-*; \
	echo "=== Verification complete ==="

## cross-compile: Build for Raspberry Pi (ARM64 Linux)
cross-compile:
	GOOS=linux GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY)-linux-arm64 ./cmd/server
	@echo "Built $(BINARY)-linux-arm64"

## run: Build and run in development mode
run: build
	./$(BINARY) --log-level debug

## clean: Remove build artifacts
clean:
	rm -f $(BINARY) $(BINARY)-linux-arm64
	rm -rf /tmp/comms-*

## lint: Run go vet
lint:
	go vet ./...
