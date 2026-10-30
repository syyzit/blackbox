BIN := bin

.PHONY: build test vet demo clean fmt

build:
	@mkdir -p $(BIN)
	go build -o $(BIN)/blackbox ./cmd/blackbox
	go build -o $(BIN)/fakeserver ./cmd/fakeserver

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

demo: build
	./scripts/demo.sh

clean:
	rm -rf $(BIN)
