FROM distribution/golem-runner:base

RUN apk add --no-cache \
                bzr \
                go \
                mercurial

ENV GOROOT /usr/lib/go
ENV GOPATH /gopath
ENV GOBIN /gopath/bin
ENV PATH $PATH:$GOROOT/bin:$GOPATH/bin

