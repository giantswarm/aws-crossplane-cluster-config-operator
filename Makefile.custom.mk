# Image URL to use all building/pushing image targets
IMG ?= gsoci.azurecr.io/giantswarm/aws-crossplane-cluster-config-operator:dev

VALUES=$(shell find ./helm -maxdepth 2 -name values.yaml)
VALUES_SCHEMA=$(shell find ./helm -maxdepth 2 -name values.schema.json)
CHART_README=$(shell find ./helm -maxdepth 2 -name README.md)

# Substitute colon with space - this creates a list.
# Word selects the n-th element of the list
IMAGE_REPO = $(word 1,$(subst :, ,$(IMG)))
IMAGE_TAG = $(word 2,$(subst :, ,$(IMG)))

CLUSTER ?= acceptance
MANAGEMENT_CLUSTER_NAME ?= test-mc
MANAGEMENT_CLUSTER_NAMESPACE ?= test

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test-unit
test-unit: ginkgo generate fmt vet envtest ## Run unit tests
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" $(GINKGO) -p --nodes 4 -r -randomize-all --randomize-suites --skip-package=tests --cover --coverpkg=`go list ./... | grep -v fakes | tr '\n' ','` ./...

.PHONY: deploy
deploy: ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	KUBECONFIG=$(KUBECONFIG) helm upgrade --install \
		--namespace giantswarm \
		--set image.tag=$(IMAGE_TAG) \
		--wait \
		aws-crossplane-cluster-config-operator helm/aws-crossplane-cluster-config-operator

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s  specified in ~/.kube/config.
	KUBECONFIG="$(KUBECONFIG)" helm uninstall \
		--namespace giantswarm \
		aws-crossplane-cluster-config-operator

.PHONY: normalize-schema
normalize-schema: ## Normalize the JSON schema
	go install github.com/giantswarm/schemalint/v2@v2
	schemalint normalize $(VALUES_SCHEMA) -o $(VALUES_SCHEMA) --force

.PHONY: validate-schema
validate-schema: ## Validate the JSON schema
	go install github.com/giantswarm/schemalint/v2@v2
	schemalint verify $(VALUES_SCHEMA)

.PHONY: generate-docs
generate-docs: ## Generate values documentation from schema
	go install github.com/giantswarm/schemadocs@latest
	schemadocs generate $(VALUES_SCHEMA) -o $(CHART_README)

.PHONY: generate-values
generate-values: ## Generate values.yaml from schema
	go install github.com/giantswarm/helm-values-gen@v1
	helm-values-gen $(VALUES_SCHEMA) -o $(VALUES) --force

.PHONY: generate-helm
generate-helm: normalize-schema validate-schema generate-docs generate-values

.PHONY: coverage-html
coverage-html: test-unit
	go tool cover -html coverprofile.out

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.10.0)

ENVTEST = $(shell pwd)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	$(call go-get-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@latest)

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} .

GINKGO = $(shell pwd)/bin/ginkgo
.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary.
	$(call go-get-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo@latest)

CLUSTERCTL = $(shell pwd)/bin/clusterctl
.PHONY: clusterctl
clusterctl: ## Download clusterctl locally if necessary.
	$(call go-get-tool,$(CLUSTERCTL),sigs.k8s.io/cluster-api/cmd/clusterctl@latest)

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef
