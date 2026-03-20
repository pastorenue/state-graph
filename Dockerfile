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

FROM gcr.io/distroless/static:nonroot
COPY --from=build-go /app/orchestrator /orchestrator
COPY --from=build-ui /ui/build /ui/build
USER nonroot:nonroot
ENTRYPOINT ["/orchestrator"]
