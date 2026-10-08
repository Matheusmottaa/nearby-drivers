# Build context: raiz do repositório
# docker build -f deploy/ride-service/Dockerfile -t nearby-drivers/ride-service .

FROM golang:1.24.3-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ride-service ./cmd/ride-service
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ride-service ./cmd/ride-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ride-service /ride-service

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/ride-service"]
