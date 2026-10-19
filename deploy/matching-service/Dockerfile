# Build context: raiz do repositório
# docker build -f deploy/matching-service/Dockerfile -t nearby-drivers/matching-service .

FROM golang:1.24.3-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/matching-service ./cmd/matching-service
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/matching-service ./cmd/matching-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/matching-service /matching-service

# /healthz, /readyz e /metrics; não fica atrás do nginx público
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/matching-service"]
