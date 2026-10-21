APP := opencode2api
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) -X main.branch=$(BRANCH)

.PHONY: fmt test vet build release-snapshot clean web-build web-dev web-dev-stop

fmt:
	gofmt -w main.go

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) .

release-snapshot:
	VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) ./scripts/build-release.sh

clean:
	rm -rf bin dist

# ---- web self-service key service (web/) ----
# web-build: build the web self-service binary to web.exe (zero external deps, pure Go stdlib)
web-build:
	go build -trimpath -o web.exe ./web

# web-dev: start the DEV instance, listening on 127.0.0.1 ONLY.
# The dev instance has no nginx in front and no Basic Auth layer.
# Once it binds 0.0.0.0, any host on the same LAN can reach the admin panel
# and the key-claim endpoint directly (finding 547 / fact 589, 592).
# -require-loopback makes the process REFUSE to start on a non-loopback address.
web-dev: web-build
	./web.exe -data-dir ./dev-data -listen 127.0.0.1 -require-loopback

# web-dev-stop: stop the dev instance by PORT.
# Do NOT use "taskkill /IM web-server.exe" - it also kills the production instance.
web-dev-stop:
	@powershell -NoProfile -Command "$$p=(Get-NetTCPConnection -LocalPort 23099 -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1).OwningProcess; if($$p){Stop-Process -Id $$p -Force; Write-Host ('stopped dev instance pid ' + $$p)} else {Write-Host 'dev instance not running'}"
