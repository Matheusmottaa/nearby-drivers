SERVICES := ride-service driver-service matching-service routing-service
IMAGE_PREFIX ?= nearby-drivers
TAG ?= latest
BIN_DIR := bin
K8S_NS := nearby-drivers
METRICS_SERVER_URL := https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml

.PHONY: help build test vet fmt tidy clean docker-build k8s-check k8s-up k8s-load k8s-rides k8s-down $(addprefix build-,$(SERVICES)) $(addprefix docker-build-,$(SERVICES)) $(addprefix run-,$(SERVICES))

help:
	@echo "build                  compila os 4 serviços em $(BIN_DIR)/"
	@echo "build-<serviço>        compila um serviço"
	@echo "run-<serviço>          executa um serviço com go run"
	@echo "test                   go test com -race"
	@echo "vet | fmt | tidy       verificações e manutenção do módulo"
	@echo "docker-build           gera as 4 imagens"
	@echo "docker-build-<serviço> gera a imagem de um serviço"
	@echo "k8s-up                 ride, driver, matching, routing + Grafana no Kubernetes"
	@echo "k8s-load               roda o Job do k6 contra o routing no cluster"
	@echo "k8s-rides              k6 com DRIVERS motoristas e PASSENGERS solicitantes (30 e 8)"
	@echo "k8s-down               remove o namespace $(K8S_NS)"
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

k8s-check:
	@test "$$(kubectl config current-context)" = docker-desktop || \
		{ echo "kubectl precisa do contexto docker-desktop: kubectl config use-context docker-desktop"; exit 1; }
	@kubectl top nodes >/dev/null 2>&1 || { \
		kubectl get deployment metrics-server -n kube-system >/dev/null 2>&1 || kubectl apply -f $(METRICS_SERVER_URL); \
		kubectl get deployment metrics-server -n kube-system -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q kubelet-insecure-tls || \
			kubectl patch deployment metrics-server -n kube-system --type=json \
				-p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'; \
		kubectl rollout status deployment/metrics-server -n kube-system --timeout=180s; \
		for i in $$(seq 24); do kubectl top nodes >/dev/null 2>&1 && break; sleep 5; done; \
		kubectl top nodes >/dev/null; }

k8s-up: k8s-check docker-build
	kubectl delete job kafka-init -n $(K8S_NS) --ignore-not-found --wait=true
	kubectl kustomize --load-restrictor LoadRestrictionsNone deploy/k8s | kubectl apply -f -
	kubectl rollout restart deployment/ride deployment/driver deployment/matching deployment/routing -n $(K8S_NS)
	for d in postgres redis kafka ride driver matching routing kube-state-metrics prometheus grafana; do \
		kubectl rollout status deployment/$$d -n $(K8S_NS) --timeout=300s || exit 1; done
	@echo "Grafana: http://localhost:30300 (admin / admin)"

k8s-load: k8s-check
	kubectl delete job k6-eta-batch -n $(K8S_NS) --ignore-not-found --wait=true
	kubectl apply -k deploy/k6
	kubectl wait --for=condition=Ready pod -l job-name=k6-eta-batch -n $(K8S_NS) --timeout=120s
	kubectl logs -f job/k6-eta-batch -n $(K8S_NS)

DRIVERS ?= 30
PASSENGERS ?= 8
HOLD ?= 3m

k8s-rides: k8s-check
	kubectl delete job k6-rides -n $(K8S_NS) --ignore-not-found --wait=true
	kubectl kustomize deploy/k6-rides | sed \
		-e '/name: DRIVERS/{n;s/value: .*/value: "$(DRIVERS)"/;}' \
		-e '/name: PASSENGERS/{n;s/value: .*/value: "$(PASSENGERS)"/;}' \
		-e '/name: HOLD/{n;s/value: .*/value: "$(HOLD)"/;}' | kubectl apply -f -
	kubectl wait --for=condition=Ready pod -l job-name=k6-rides -n $(K8S_NS) --timeout=180s
	kubectl logs -f job/k6-rides -n $(K8S_NS)

k8s-down:
	kubectl delete namespace $(K8S_NS) --ignore-not-found
	kubectl delete clusterrole,clusterrolebinding nearby-drivers-prometheus nearby-drivers-kube-state-metrics --ignore-not-found

clean:
	rm -rf $(BIN_DIR)
