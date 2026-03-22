FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /example ./examples/06-kubernetes

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /example /example
USER nonroot:nonroot
ENTRYPOINT ["/example"]
