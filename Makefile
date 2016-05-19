# building the rbd docker plugin golang binary with version
# makefile mostly used for packing a tpkg

.PHONY: all build install clean test version setup systemd

IMAGE_PATH=ypengineering/rbd-docker-plugin
TAG?=latest
IMAGE=$(IMAGE_PATH):$(TAG)
SUDO?=


TMPDIR?=/tmp
INSTALL?=install


BINARY=rbd-docker-plugin
PKG_SRC=main.go driver.go version.go
PKG_SRC_TEST=$(PKG_SRC) driver_test.go unlock_test.go

PACKAGE_BUILD=$(TMPDIR)/$(BINARY).tpkg.buildtmp

PACKAGE_BIN_DIR=$(PACKAGE_BUILD)/reloc/bin
PACKAGE_ETC_DIR=$(PACKAGE_BUILD)/reloc/etc
PACKAGE_CRON_DIR=$(PACKAGE_ETC_DIR)/cron.d
PACKAGE_INIT_DIR=$(PACKAGE_ETC_DIR)/init
PACKAGE_LOG_CONFIG_DIR=$(PACKAGE_ETC_DIR)/logrotate.d
PACKAGE_SYSTEMD_DIR=$(PACKAGE_ETC_DIR)/systemd/system

CONFIG_FILES=tpkg.yml README.md LICENSE
CRON_JOB=etc/cron.d/rbd-docker-plugin-checks
SYSTEMD_UNIT=etc/systemd/rbd-docker-plugin.service
UPSTART_INIT=etc/init/rbd-docker-plugin.conf
LOG_CONFIG=etc/logrotate.d/rbd-docker-plugin_logrotate
SCRIPT_FILES=postinstall postremove
BIN_FILES=dist/$(BINARY) check-ceph-rbd-docker-plugin.sh

# Run these if you have a local dev env setup, otherwise they will / can be run
# in the container.
all: build

# set VERSION from version.go, eval into Makefile for inclusion into tpkg.yml
version: version.go
	$(eval VERSION := $(shell grep "VERSION" version.go | cut -f2 -d'"'))

build: dist/$(BINARY)

dist/$(BINARY): $(PKG_SRC)
	go build -v -x -o dist/$(BINARY) .

install: build test
	go install .

clean:
	go clean
	rm -f dist/$(BINARY)

uninstall:
	@$(RM) -iv `which $(BINARY)`

# FIXME: TODO: this micro-osd script leaves ceph-mds laying around -- fix it up
test:
	TMP_DIR=$$(mktemp -d) && \
		./micro-osd.sh $$TMP_DIR && \
		export CEPH_CONF=$${TMP_DIR}/ceph.conf && \
		ceph -s && \
		go test -v && \
		rm -rf $$TMP_DIR


# use existing ceph installation instead of micro-osd.sh - expecting CEPH_CONF to be set ...
CEPH_CONF ?= /etc/ceph/ceph.conf
local_test:
	@echo "Using CEPH_CONF=$(CEPH_CONF)"
	test -n "${CEPH_CONF}" && \
		ceph -s && \
		go test -v

dist:
	mkdir dist

systemd: dist
	cp systemd/rbd-docker-plugin.service dist/



# Used to have build env be inside container and to pull out the binary.
make/%: build_docker
	$(SUDO) docker run ${DOCKER_ARGS} --rm -i $(IMAGE) make $*

run:
	$(SUDO) docker run ${DOCKER_ARGS} --rm -it $(IMAGE)

build_docker:
	$(SUDO) docker build -t $(IMAGE) .

binary_from_container:
	$(SUDO) docker run ${DOCKER_ARGS} --rm -it \
		-v $${PWD}:/rbd-docker-plugin/dist \
		-w /rbd-docker-plugin \
		$(IMAGE) make build

local:
	$(SUDO) docker run ${DOCKER_ARGS} --rm -it \
		-v $${PWD}:/rbd-docker-plugin \
		-w /rbd-docker-plugin \
		$(IMAGE)


# container actions
test_from_container: make/test



# build relocatable tpkg
# TODO: repair PATHS at install to set TPKG_HOME (assumed /home/ops)
package: version build local_test
	$(RM) -fr $(PACKAGE_BUILD)
	mkdir -p $(PACKAGE_BIN_DIR) $(PACKAGE_INIT_DIR) $(PACKAGE_SYSTEMD_DIR) $(PACKAGE_LOG_CONFIG_DIR) $(PACKAGE_CRON_DIR)
	$(INSTALL) $(SCRIPT_FILES) $(PACKAGE_BUILD)/.
	$(INSTALL) $(BIN_FILES) $(PACKAGE_BIN_DIR)/.
	$(INSTALL) -m 0644 $(CONFIG_FILES) $(PACKAGE_BUILD)/.
	$(INSTALL) -m 0644 $(CRON_JOB) $(PACKAGE_CRON_DIR)/.
	$(INSTALL) -m 0644 $(SYSTEMD_UNIT) $(PACKAGE_SYSTEMD_DIR)/.
	$(INSTALL) -m 0644 $(UPSTART_INIT) $(PACKAGE_INIT_DIR)/.
	$(INSTALL) -m 0644 $(LOG_CONFIG) $(PACKAGE_LOG_CONFIG_DIR)/.
	sed -i "s/^version:.*/version: $(VERSION)/" $(PACKAGE_BUILD)/tpkg.yml
	tpkg --make $(PACKAGE_BUILD) --out $(CURDIR)
