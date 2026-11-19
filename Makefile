IMAGE ?= icr.io/vopo/moca-sbx-enrollment-sync
TAG ?= latest
DOCKER ?= docker

.PHONY: build test vet image fmt deploy-epoc001 deploy-epoc002 dry-run

build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o enrollment-sync ./cmd/enrollment-sync

test:
	GOTOOLCHAIN=local go test ./...

vet:
	GOTOOLCHAIN=local go vet ./...

fmt:
	gofmt -w .

image: build
	$(DOCKER) build -t $(IMAGE):$(TAG) . && $(DOCKER) push $(IMAGE):$(TAG)

deploy-epoc001:
	kubectl --kubeconfig ~/.kube/config-epoc001 -n moca-sbx apply -k deploy/epoc001

deploy-epoc002:
	kubectl --kubeconfig ~/.kube/config-epoc002 -n moca-sbx apply -k deploy/epoc002

# A local dry run against a live deployment (no CronJob needed):
#   make dry-run SITE=epoc001 KUBECONFIG_FILE=~/.kube/config-epoc001
dry-run:
	kubectl --kubeconfig $(KUBECONFIG_FILE) -n moca-sbx get secret moca-mu1 \
	  -o jsonpath='{.data.SH_INSTALLER_TOKEN}' | base64 -d > /tmp/.itok && \
	SITE=$(SITE) ENROLLMENT_REPO_URL=$(CURDIR) CONTROL_PLANE_URL=$(CP_URL) \
	SH_INSTALLER_TOKEN=$$(cat /tmp/.itok) ./enrollment-sync --dry-run; \
	rc=$$?; rm -f /tmp/.itok; exit $$rc
