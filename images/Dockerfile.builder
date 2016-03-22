FROM alpine:3.3

RUN apk add --no-cache \
                bash \
                curl \
                git \
                go \
                xz

ENV GOROOT /usr/lib/go
ENV GOPATH /gopath
ENV GOBIN /gopath/bin
ENV PATH $PATH:$GOROOT/bin:$GOPATH/bin

# Install godep
RUN go get github.com/tools/godep

