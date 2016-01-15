From golang:1.4

COPY . /go/src/github.com/docker/golem/malevolent

ENV GOPATH /go/src/github.com/docker/golem/malevolent/Godeps/_workspace:$GOPATH

RUN go install github.com/docker/golem/malevolent

ENTRYPOINT [ "malevolent" ]
