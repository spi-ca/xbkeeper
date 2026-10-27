VERSION := v20261027-1
GO ?= go
DIST := dist/xbkeeper-$(VERSION).tar.gz
GO_SOURCES := $(sort $(wildcard cmd/xbkeeper/*.go internal/cli/*.go internal/backup/*.go internal/backup/report/*.go tests/packaging/*.go))
DOC_SOURCES := README.md docs/cli.md docs/operations.md docs/development.md docs/integrity.md docs/systemd-migration.md docs/toml-migration.md examples/README.md examples/xbkeeper.toml examples/xbkeeper.cnf packaging/arch/README.md
SOURCES := go.mod go.sum $(GO_SOURCES) $(DOC_SOURCES) LICENSE LICENSES/go-toml-MIT.txt Makefile packaging/systemd/xbkeeper.service packaging/systemd/xbkeeper.timer packaging/tmpfiles/xbkeeper.conf

.PHONY: build test source-list dist clean
build:
	mkdir -p build
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "-X main.Version=$(VERSION)" -o build/xbkeeper ./cmd/xbkeeper

test:
	$(GO) test ./...
	$(GO) test -race ./...
	$(GO) vet ./...

# Inspect the complete allowlist without creating a release artifact.
source-list:
	@printf '%s\n' $(SOURCES)

dist:
	mkdir -p dist
	bash -o pipefail -ec 'tmp="$(DIST).tmp"; trap '\''rm -f "$$tmp"'\'' EXIT; tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --mode=0644 --transform="s,^,xbkeeper-$(VERSION)/," -cf - $(SOURCES) | gzip -n > "$$tmp"; mv "$$tmp" "$(DIST)"'
	cp $(DIST) packaging/arch/
	sha256sum $(DIST)

clean:
	rm -rf build dist
