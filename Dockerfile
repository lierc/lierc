FROM golang
COPY . /go/src/github.com/lierc/lierc
WORKDIR /go/src/github.com/lierc/lierc
RUN go install -v ./cmd/*
