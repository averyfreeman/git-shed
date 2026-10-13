.PHONY: build test check install clean
build:
	mkdir -p bin
	go build -trimpath -o bin/git-shed .
test:
	go test ./...
check:
	go vet ./...
	go test -race ./...
install:
	mkdir -p "$(HOME)/.local/bin"
	go build -trimpath -o "$(HOME)/.local/bin/git-shed" .
clean:
	rm -rf bin
