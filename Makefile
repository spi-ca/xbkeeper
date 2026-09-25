VERSION := 0.2.0
GO ?= go
DIST := dist/xbkeeper-$(VERSION).tar.gz
SOURCES := go.mod $(sort $(wildcard *.go)) README.md LICENSE Makefile docs/integrity.md packaging/arch/README.md

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
	bash -o pipefail -ec 'tmp="$(DIST).tmp"; trap '\''rm -f "$$tmp"'\'' EXIT; tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --transform="s,^,xbkeeper-$(VERSION)/," -cf - $(SOURCES) | gzip -n > "$$tmp"; mv "$$tmp" "$(DIST)"'
	cp $(DIST) packaging/arch/
	sha256sum $(DIST)

clean:
	rm -rf build dist
