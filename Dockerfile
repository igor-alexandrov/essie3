FROM golang:1.26.2-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o essie3 .

FROM alpine:3.23
COPY --from=builder /app/essie3 /usr/local/bin/
ENTRYPOINT ["essie3"]
