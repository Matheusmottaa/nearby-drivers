# Build context: raiz do repositório
# docker build -f deploy/driver-service/Dockerfile -t nearby-drivers/driver-service .

FROM golang:1.24.3-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/driver-service ./cmd/driver-service
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/driver-service ./cmd/driver-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/driver-service /driver-service

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/driver-service"]
