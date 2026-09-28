PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X main.version=$(or $(VERSION),dev)

mp: *.go go.mod go.sum
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o mp .

install: mp
	install -d $(PREFIX)/bin
	install -m 755 mp $(PREFIX)/bin/mp

uninstall:
	rm -f $(PREFIX)/bin/mp

clean:
	rm -f mp

.PHONY: install uninstall clean
