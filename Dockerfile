FROM golang AS builder

WORKDIR /lierc

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go install -ldflags="-s -w" ./cmd/...

FROM scratch

COPY --from=builder /go/bin/lierc* /go/bin/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
