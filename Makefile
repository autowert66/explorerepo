.PHONY: build test live vet
build:
	go build -o bin/explorerepo ./cmd/explorerepo
test:
	go test ./...
race:
	go test -race ./...
live:
	EXPLOREREPO_LIVE=1 go test ./cmd/explorerepo -run Live -v -count=1
vet:
	gofmt -l . && go vet ./...
