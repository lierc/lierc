FROM golang
RUN go get github.com/tools/godep
COPY . /go/src/github.com/lierc/lierc
WORKDIR /go/src/github.com/lierc/lierc
RUN godep go install -v ./cmd/*
