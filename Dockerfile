FROM --platform=$BUILDPLATFORM golang AS builder

ARG TARGETOS TARGETARCH

WORKDIR /lierc

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/ ./cmd/...

FROM scratch

COPY --from=builder /out/ /go/bin/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENV PATH="/go/bin:${PATH}"

CMD ["liercd"]
