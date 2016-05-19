.PHONY: $(CONTAINER)

SUDO?=
MARATHON_HOST?=localhost
MARATHON_PASS?=
MARATHON_JSON?=marathon-test.json

# update this when you make changes
APP_VERSION=0.2.1
CONTAINER=rbd-marathon-test
TAG?=latest
REMOTE_NAME?=$(CONTAINER)

#all: remote
#remote:	$(REMOTE_NAME)

container:
	$(SUDO) docker build -t $(REMOTE_NAME):$(APP_VERSION) .

push: container
	$(SUDO) docker tag -f $(REMOTE_NAME):$(APP_VERSION) $(REMOTE_NAME):$(TAG)
	$(SUDO) docker push $(REMOTE_NAME):$(APP_VERSION)
	$(SUDO) docker push $(REMOTE_NAME):$(TAG)

deploy:
	curl -X POST -H "Content-Type: application/json" http://$(MARATHON_HOST):8080/v2/apps -d@$(MARATHON_JSON)
