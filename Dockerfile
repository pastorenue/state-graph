FROM golang:1.22-alpine AS build-go
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o orchestrator ./cmd/orchestrator

FROM node:22-alpine AS build-ui
WORKDIR /ui
COPY ui/package*.json ./
RUN npm ci
COPY ui/ .
RUN npm run build

# Create /home/nonroot with correct ownership for the nonroot user (UID 65532).
FROM alpine:3 AS setup-home
RUN addgroup -g 65532 nonroot && \
    adduser -u 65532 -G nonroot -h /home/nonroot -D nonroot && \
    mkdir -p /home/nonroot/.kube && \
    chown -R 65532:65532 /home/nonroot

FROM gcr.io/distroless/static:nonroot
COPY --from=setup-home /home/nonroot /home/nonroot
COPY --from=build-go /app/orchestrator /orchestrator
COPY --from=build-ui /ui/build /ui/build
USER nonroot:nonroot
ENTRYPOINT ["/orchestrator"]
