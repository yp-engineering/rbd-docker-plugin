.PHONY: $(CONTAINER)

SUDO ?=
MARATHON_HOST ?= localhost
MARATHON_PASS ?=

# update this when you make changes
APP_VERSION=0.1.1
CONTAINER=rbd-marathon-test
TAG?=latest
REMOTE_NAME=$(CONTAINER):$(TAG)

all: remote
remote:	$(REMOTE_NAME)

$(CONTAINER):
	$(SUDO) docker build -t $(CONTAINER):$(APP_VERSION) .

$(REMOTE_NAME): $(CONTAINER)
	$(SUDO) docker tag -f $(CONTAINER):$(APP_VERSION) $(REMOTE_NAME):$(APP_VERSION)
	$(SUDO) docker push $(REMOTE_NAME):$(APP_VERSION)

deploy: $(REMOTE_NAME)
	curl -X POST --user "admin:$(MARATHON_PASS)" -H "Content-Type: application/json" \
	    http://$(MARATHON_HOST):8080/v2/apps \
	    -d@marathon-test.json

