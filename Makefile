ROOTDIR=$(abspath .)
BASEDIR=./images
DOCKER=docker
NAMESPACE=distribution
GOFILES=golem.go $(wildcard runner/*.go)

builderimage:
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.builder -t $(NAMESPACE)/golem-builder:latest .

$(BASEDIR)/golem: builderimage $(GOFILES)
	$(DOCKER) run -v $(ROOTDIR):/gopath/src/github.com/docker/golem $(NAMESPACE)/golem-builder sh -c "cd /gopath/src/github.com/docker/golem; godep go build -o $(BASEDIR)/golem ."

baseimage: $(BASEDIR)/golem
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.base -t $(NAMESPACE)/golem-runner:base  .

batsimage: baseimage
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.bats -t $(NAMESPACE)/golem-runner:bats  .

golangimage: baseimage
	cd $(BASEDIR);\
	$(DOCKER) build -f Dockerfile.golang -t $(NAMESPACE)/golem-runner:golang  .

.PHONY: builderimage baseimage batsimage golangimage
