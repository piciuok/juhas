# juhas — build for macOS (cgo, menu bar) and Linux (pure Go, DBus tray).
BIN      := juhas
DIST     := dist
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build test vet install dist clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/juhas

test:
	go test ./...

vet:
	go vet ./...
	GOOS=linux CGO_ENABLED=0 go vet ./...

# Copies the binary to ~/.local/bin and enables start at login.
install: build
	./bin/$(BIN) install

# Release binaries. macOS needs cgo (Xcode command line tools), so build the
# darwin ones on a Mac; Linux builds anywhere.
dist: clean
	mkdir -p $(DIST)
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-linux-amd64 ./cmd/juhas
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-linux-arm64 ./cmd/juhas
	@if [ "$$(uname)" = Darwin ]; then \
	  GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-darwin-arm64 ./cmd/juhas && \
	  GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-darwin-amd64 ./cmd/juhas; \
	fi
	ls -l $(DIST)

clean:
	rm -rf bin $(DIST)
