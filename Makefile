SHELL := bash
export CGO_ENABLED := 0

GO_DIRS := ./cmd/... ./internal/...

.PHONY: check build test lint fmt-check linecount web

check: fmt-check vet lint test linecount

web:
	cd web && bun install --frozen-lockfile && bun run build
	node -e "const fs=require('fs');fs.rmSync('internal/adapter/httpapi/dist',{recursive:true,force:true});fs.cpSync('web/build','internal/adapter/httpapi/dist',{recursive:true});fs.writeFileSync('internal/adapter/httpapi/dist/.gitkeep','');"

fmt-check:
	@unformatted="$$(gofmt -l cmd internal)"; \
	if [[ -n "$${unformatted}" ]]; then \
		echo "Unformatted Go files:" >&2; \
		echo "$${unformatted}" >&2; \
		exit 1; \
	fi

vet:
	go vet $(GO_DIRS)

lint:
	golangci-lint run $(GO_DIRS)

test:
	go test $(GO_DIRS)

linecount:
	bash scripts/linecount.sh .
	bash scripts/linecount_test.sh >/dev/null

build: web
	go build -o bin/max ./cmd/max
