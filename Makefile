PREFIX?=$(shell pwd)
BASEDIR=./images
DOCKER=docker
NAMESPACE=distribution
GOFILES=golem.go $(wildcard **/*.go)

.PHONY: builderimage baseimage batsimage golangimage golemimage fmt lint vet binaries build install clean all
.DEFAULT: all
all: fmt lint vet binaries

$(PREFIX)/bin/golem:
	@echo "+ $@"
	@go build -o $@ ${GO_LDFLAGS}  ${GO_GCFLAGS} .

builderimage:
	@echo "+ $@"
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.builder -t $(NAMESPACE)/golem-builder:latest .

$(BASEDIR)/golem: builderimage $(GOFILES)
	@echo "+ $@"
	$(DOCKER) run -v $(PREFIX):/gopath/src/github.com/docker/golem $(NAMESPACE)/golem-builder sh -c "cd /gopath/src/github.com/docker/golem; GO15VENDOREXPERIMENT=1 go build -o $(BASEDIR)/golem ."

baseimage: $(BASEDIR)/golem
	@echo "+ $@"
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.base -t $(NAMESPACE)/golem-runner:base  .

batsimage: baseimage
	@echo "+ $@"
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.bats -t $(NAMESPACE)/golem-runner:bats  .

golangimage: baseimage
	@echo "+ $@"
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.golang -t $(NAMESPACE)/golem-runner:golang  .

golemimage: $(BASEDIR)/golem
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile -t $(NAMESPACE)/golem:latest  .

fmt:
	@echo "+ $@"
	@test -z "$$(gofmt -s -l . | grep -v Godeps/_workspace/src/ | tee /dev/stderr)" || \
		echo "+ please format Go code with 'gofmt -s'"

lint:
	@echo "+ $@"
	@test -z "$$(golint ./... | grep -v Godeps/_workspace/src/ | tee /dev/stderr)"

build:
	@echo "+ $@"
	@go build -v ${GO_LDFLAGS} .

install:
	@echo "+ $@"
	@go install -v ${GO_LDFLAGS} .

binaries: ${PREFIX}/bin/golem
	@echo "+ $@"

vet: binaries
	@echo "+ $@"
	@go vet ./...

clean: builderimage
	@echo "+ $@"
	@rm -rf "${PREFIX}/bin/golem"

	$(DOCKER) run -v $(PREFIX):/gopath/src/github.com/docker/golem $(NAMESPACE)/golem-builder sh -c "cd /gopath/src/github.com/docker/golem; rm -f $(BASEDIR)/golem"
