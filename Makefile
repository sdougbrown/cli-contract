.PHONY: test verify

test:
	go test ./...

verify: vet test

vet:
	go vet ./...
