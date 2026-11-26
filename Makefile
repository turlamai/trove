# Trove build entry points.
.PHONY: daemon daemon-test daemon-vet frontend build lint

VERSION ?= $(shell date +%Y.%m.%d)

daemon:
	cd daemon && go build ./...

daemon-test:
	cd daemon && go test ./...

daemon-vet:
	cd daemon && go vet ./...

frontend:
	cd frontend && npm ci --no-fund --no-audit && npm run build

build:
	./scripts/build.sh $(VERSION)

lint:
	cd daemon && go vet ./...
	cd frontend && npx tsc --noEmit
