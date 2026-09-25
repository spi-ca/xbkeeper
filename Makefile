VERSION := v20260926-7
GO ?= go
DIST := dist/xbkeeper-$(VERSION).tar.gz
SOURCES := go.mod go.sum $(sort $(wildcard *.go)) README.md LICENSE LICENSES/go-toml-MIT.txt Makefile docs/integrity.md docs/systemd-migration.md docs/toml-migration.md examples/README.md examples/xbkeeper.toml examples/xbkeeper.cnf packaging/arch/README.md packaging/systemd/xbkeeper.service packaging/systemd/xbkeeper.timer packaging/tmpfiles/xbkeeper.conf

.PHONY: build test dist clean
build:
	mkdir -p build
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "-X main.Version=$(VERSION)" -o build/xbkeeper .

test:
	$(GO) test ./...
	$(GO) test -race ./...
	$(GO) vet ./...

dist:
	mkdir -p dist
	bash -o pipefail -ec 'tmp="$(DIST).tmp"; trap '\''rm -f "$$tmp"'\'' EXIT; tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --mode=0644 --transform="s,^,xbkeeper-$(VERSION)/," -cf - $(SOURCES) | gzip -n > "$$tmp"; mv "$$tmp" "$(DIST)"'
	cp $(DIST) packaging/arch/
	sha256sum $(DIST)

clean:
	rm -rf build dist
