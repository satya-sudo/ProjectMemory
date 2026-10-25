export GOCACHE := $(CURDIR)/.cache/go-build
export GOMODCACHE := $(CURDIR)/.cache/go-mod
export PATH := /Applications/Docker.app/Contents/Resources/bin:$(PATH)
DOCKER ?= $(shell command -v docker 2>/dev/null || printf '%s' /Applications/Docker.app/Contents/Resources/bin/docker)

.PHONY: run migrate bootstrap test test-db verify-local vet build db-up db-stop db-restart db-status
run:
	go run ./cmd/projectmap serve
migrate:
	go run ./cmd/projectmap migrate
bootstrap:
	go run ./cmd/projectmap bootstrap
test:
	go test -race ./...
test-db:
	@test -n "$$PROJECTMAP_TEST_DATABASE_URL" || (echo 'Set PROJECTMAP_TEST_DATABASE_URL before running database checks.'; exit 1)
	go test -race -count=1 -v ./internal/database
vet:
	go vet ./...
build:
	go build -o .local/projectmap ./cmd/projectmap
verify-local: build
	python3 scripts/verify_local.py
db-up:
	"$(DOCKER)" compose up -d --wait postgres
db-stop:
	"$(DOCKER)" compose stop postgres
db-restart:
	"$(DOCKER)" compose restart postgres
db-status:
	"$(DOCKER)" compose ps
