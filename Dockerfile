FROM golang:alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o llm-gateway .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/llm-gateway /usr/local/bin/llm-gateway

EXPOSE 8080
ENTRYPOINT ["llm-gateway"]
