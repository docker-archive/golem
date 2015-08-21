From golang:1.4

COPY . /go/src/github.com/dmcgowan/golem/malevolent

ENV GOPATH /go/src/github.com/dmcgowan/golem/malevolent/Godeps/_workspace:$GOPATH

RUN go install github.com/dmcgowan/golem/malevolent

ENTRYPOINT [ "malevolent" ]
