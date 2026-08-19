.PHONY: test vet lint release-binaries

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

release-binaries:
	bash scripts/build-release.sh
