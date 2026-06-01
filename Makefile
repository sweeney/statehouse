.PHONY: build test lint lint-spec clean deploy

BINARY := statehouse
MAIN   := ./cmd/statehouse

build:
	go build -o bin/$(BINARY) $(MAIN)

test:
	go test -race -count=1 ./...

lint:
	go vet ./...

lint-spec:
	npx --yes @stoplight/spectral-cli@latest lint internal/httpapi/openapi.yaml

clean:
	rm -rf bin/

DEPLOY_HOST ?= sweeney@garibaldi

deploy:
	./deploy/deploy.sh $(DEPLOY_HOST)
