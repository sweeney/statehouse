.PHONY: build test lint clean deploy

BINARY := statehouse
MAIN   := ./cmd/statehouse

build:
	go build -o bin/$(BINARY) $(MAIN)

test:
	go test -race -count=1 ./...

lint:
	go vet ./...

clean:
	rm -rf bin/

DEPLOY_HOST ?= sweeney@garibaldi

deploy:
	./deploy/deploy.sh $(DEPLOY_HOST)
