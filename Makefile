GO ?= go
BIN := bin/kpf
PKG := ./...

.PHONY: build run test tidy fmt vet clean daemon-start daemon-stop ping

build:
	@mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/kpf

run: build
	./$(BIN)

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt $(PKG)

vet:
	$(GO) vet $(PKG)

test:
	$(GO) test $(PKG)

daemon-start: build
	./$(BIN) daemon start

daemon-stop: build
	./$(BIN) daemon stop

ping: build
	./$(BIN) ping

clean:
	rm -rf bin
