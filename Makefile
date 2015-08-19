# building the rbd docker plugin golang binary with version
# makefile mostly used for packing a tpkg

.PHONY: all build install clean test version setup

TMPDIR ?= /tmp
INSTALL ?= install


BINARY=rbd-docker-plugin
PKG_SRC=main.go driver.go version.go
PKG_SRC_TEST=$(PKG_SRC) driver_test.go

PACKAGE_BUILD=$(TMPDIR)/$(BINARY).tpkg.buildtmp

PACKAGE_BIN_DIR=$(PACKAGE_BUILD)/reloc/bin
PACKAGE_ETC_DIR=$(PACKAGE_BUILD)/reloc/etc
PACKAGE_INIT_DIR=$(PACKAGE_BUILD)/reloc/etc/init
PACKAGE_LOG_CONFIG_DIR=$(PACKAGE_BUILD)/reloc/etc/logrotate.d
PACKAGE_SYSTEMD_DIR=$(PACKAGE_BUILD)/reloc/etc/systemd/system

PACKAGE_SYSTEMD_UNIT=systemd/rbd-docker-plugin.service
PACKAGE_INIT=init/rbd-docker-plugin.conf
PACKAGE_LOG_CONFIG=logrotate.d/rbd-docker-plugin_logrotate
PACKAGE_CONFIG_FILES=tpkg.yml README.md LICENSE
PACKAGE_SCRIPT_FILES=postinstall postremove

all: build

# need to install the devel packages to compile go-ceph code
# TODO: test if package already installed before calling sudo
setup:
	go get -t .
	sudo yum install -y librados2-devel librbd1-devel

# set VERSION from version.go, eval into Makefile for inclusion into tpkg.yml
version: version.go
	$(eval VERSION := $(shell grep "VERSION" version.go | cut -f2 -d'"'))

build: $(BINARY)

# this just builds local binary
$(BINARY): $(PKG_SRC)
	go build -v -x .

# this will install binary in your GOPATH
install: build test
	go install .

clean:
	go clean

uninstall:
	@$(RM) -iv `which $(BINARY)`

test:
	go test

# build relocatable tpkg
# TODO: repair PATHS at install to set TPKG_HOME (assumed /home/ops)
package: version build test
	$(RM) -fr $(PACKAGE_BUILD)
	mkdir -p $(PACKAGE_BIN_DIR) $(PACKAGE_INIT_DIR) $(PACKAGE_SYSTEMD_DIR) $(PACKAGE_LOG_CONFIG_DIR)
	$(INSTALL) $(PACKAGE_SCRIPT_FILES) $(PACKAGE_BUILD)/.
	$(INSTALL) -m 0644 $(PACKAGE_CONFIG_FILES) $(PACKAGE_BUILD)/.
	$(INSTALL) -m 0644 $(PACKAGE_SYSTEMD_UNIT) $(PACKAGE_SYSTEMD_DIR)/.
	$(INSTALL) -m 0644 $(PACKAGE_INIT) $(PACKAGE_INIT_DIR)/.
	$(INSTALL) -m 0644 $(PACKAGE_LOG_CONFIG) $(PACKAGE_LOG_CONFIG_DIR)/.
	$(INSTALL) $(BINARY) $(PACKAGE_BIN_DIR)/.
	sed -i "s/version:.*/version: $(VERSION)/" $(PACKAGE_BUILD)/tpkg.yml
	tpkg --make $(PACKAGE_BUILD) --out $(CURDIR)
