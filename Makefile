SERVICES := ride-service driver-service matching-service routing-service
IMAGE_PREFIX ?= nearby-drivers
TAG ?= latest
BIN_DIR := bin

.PHONY: help build test vet fmt tidy clean docker-build etapa7 $(addprefix build-,$(SERVICES)) $(addprefix docker-build-,$(SERVICES)) $(addprefix run-,$(SERVICES))

help:
	@echo "build                  compila os 4 serviços em $(BIN_DIR)/"
	@echo "build-<serviço>        compila um serviço"
	@echo "run-<serviço>          executa um serviço com go run"
	@echo "test                   go test com -race"
	@echo "vet | fmt | tidy       verificações e manutenção do módulo"
	@echo "docker-build           gera as 4 imagens"
	@echo "docker-build-<serviço> gera a imagem de um serviço"
	@echo "etapa7                 sobe o compose e percorre o roteiro da etapa 7"
	@echo "clean                  remove $(BIN_DIR)/"

build: $(addprefix build-,$(SERVICES))

$(addprefix build-,$(SERVICES)): build-%:
	go build -trimpath -o $(BIN_DIR)/$* ./cmd/$*

$(addprefix run-,$(SERVICES)): run-%:
	go run ./cmd/$*

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w cmd internal

tidy:
	go mod tidy

docker-build: $(addprefix docker-build-,$(SERVICES))

$(addprefix docker-build-,$(SERVICES)): docker-build-%:
	docker build -f deploy/$*/Dockerfile -t $(IMAGE_PREFIX)/$*:$(TAG) .

etapa7:
	go run ./scripts/etapa7

clean:
	rm -rf $(BIN_DIR)
