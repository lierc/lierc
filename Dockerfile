FROM golang
RUN go get -u github.com/golang/dep/cmd/dep
COPY . /go/src/github.com/lierc/lierc
WORKDIR /go/src/github.com/lierc/lierc
RUN dep ensure
RUN go install -v ./cmd/*
