# Build context: raiz do repositório
# docker build -f deploy/routing-service/Dockerfile -t nearby-drivers/routing-service .

FROM golang:1.24.3-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/routing-service ./cmd/routing-service
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/routing-service ./cmd/routing-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/routing-service /routing-service

# O arquivo do grafo não entra na imagem: é montado como volume a partir de data/maps
VOLUME ["/data/maps"]

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/routing-service"]
