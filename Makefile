BINARY      := searchengine
PKG_VERSION := 1.0.0
BUILD_DIR   := build
DEB_DIR     := $(BUILD_DIR)/deb
BINARIES    := search admin crawl

.PHONY: all build test test-race cover coverage-check lint clean deb run docker

all: test build

build:
	go build -o $(BUILD_DIR)/$(BINARY)-search ./cmd/search
	go build -o $(BUILD_DIR)/$(BINARY)-admin ./cmd/admin
	go build -o $(BUILD_DIR)/$(BINARY)-crawl ./cmd/crawl

run: build
	./$(BUILD_DIR)/$(BINARY)-search

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test ./... -coverprofile=$(BUILD_DIR)/coverage.out -covermode=atomic
	go tool cover -func=$(BUILD_DIR)/coverage.out

coverage-check: cover
	@pct=$$(go tool cover -func=$(BUILD_DIR)/coverage.out | tail -1 | grep -oE '[0-9.]+'); \
	echo "Total coverage: $$pct%"; \
	awk -v p=$$pct 'BEGIN { if (p < 100.0) { print "ERROR: coverage below 100%"; exit 1 } }'

lint:
	go vet ./...
	gofmt -l .

clean:
	rm -rf $(BUILD_DIR)

deb: build
	mkdir -p $(DEB_DIR)/DEBIAN
	mkdir -p $(DEB_DIR)/usr/bin
	mkdir -p $(DEB_DIR)/lib/systemd/system
	mkdir -p $(DEB_DIR)/etc/searchengine
	mkdir -p $(DEB_DIR)/var/lib/searchengine
	for bin in $(BINARIES); do \
		cp $(BUILD_DIR)/$(BINARY)-$$bin $(DEB_DIR)/usr/bin/$(BINARY)-$$bin; \
		chmod 755 $(DEB_DIR)/usr/bin/$(BINARY)-$$bin; \
	done
	sed 's/^Version: .*/Version: $(PKG_VERSION)/' packaging/debian/control > $(DEB_DIR)/DEBIAN/control
	cp packaging/debian/postinst $(DEB_DIR)/DEBIAN/postinst
	cp packaging/debian/prerm $(DEB_DIR)/DEBIAN/prerm
	cp packaging/searchengine-search.service $(DEB_DIR)/lib/systemd/system/searchengine-search.service
	cp packaging/searchengine-admin.service $(DEB_DIR)/lib/systemd/system/searchengine-admin.service
	cp packaging/searchengine-crawl.service $(DEB_DIR)/lib/systemd/system/searchengine-crawl.service
	cp packaging/searchengine.env $(DEB_DIR)/etc/searchengine/searchengine.env
	echo "/etc/searchengine/searchengine.env" > $(DEB_DIR)/DEBIAN/conffiles
	chmod 755 $(DEB_DIR)/DEBIAN/postinst $(DEB_DIR)/DEBIAN/prerm
	dpkg-deb --build --root-owner-group $(DEB_DIR) $(BUILD_DIR)/$(BINARY)_$(PKG_VERSION)_amd64.deb
	@echo "Package built: $(BUILD_DIR)/$(BINARY)_$(PKG_VERSION)_amd64.deb"

docker:
	docker build -t $(BINARY):$(PKG_VERSION) .
