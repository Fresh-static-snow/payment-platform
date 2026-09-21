SHELL := /bin/sh

# Keep manual migration commands aligned with the values Docker Compose reads.
-include .env

GO_IMAGE ?= golang:1.27-bookworm
COMPOSE ?= docker compose
KUBECTL ?= kubectl
KUSTOMIZE_OVERLAY ?= deployments/kubernetes/overlays/local
TERRAFORM_IMAGE ?= hashicorp/terraform:1.13.5
TERRAFORM_CACHE_VOLUME ?= payment-platform-terraform-plugin-cache
AWS_CONFIG_DIR ?= $(HOME)/.aws
AWS_DOCKER_EXTRA_ARGS ?=
TF_ENV ?= dev
TF_DIR = infra/terraform/environments/$(TF_ENV)
GO_DOCKER = docker run --rm -e GOCACHE=/tmp/go-build -e GOMODCACHE=/tmp/go-mod -v "$(CURDIR):/workspace" -w /workspace $(GO_IMAGE)
AWS_DOCKER_ENV = -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN -e AWS_PROFILE -e AWS_DEFAULT_PROFILE -e AWS_REGION -e AWS_DEFAULT_REGION -e AWS_ROLE_ARN -e AWS_WEB_IDENTITY_TOKEN_FILE -e AWS_EC2_METADATA_DISABLED -e AWS_SDK_LOAD_CONFIG=1
AWS_DOCKER_CONFIG = $(if $(wildcard $(AWS_CONFIG_DIR)),-v "$(AWS_CONFIG_DIR):/root/.aws:ro" -e AWS_CONFIG_FILE=/root/.aws/config -e AWS_SHARED_CREDENTIALS_FILE=/root/.aws/credentials,)
AWS_DOCKER_FLAGS = $(AWS_DOCKER_ENV) $(AWS_DOCKER_CONFIG) $(AWS_DOCKER_EXTRA_ARGS)
DATABASE_URL ?= postgres://$(or $(POSTGRES_USER),payment):$(or $(POSTGRES_PASSWORD),payment)@postgres:5432/$(or $(POSTGRES_DB),payments)?sslmode=disable

.PHONY: up down build logs test test-unit test-integration test-e2e migrate-up migrate-down lint proto config k8s-render k8s-apply k8s-delete k8s-aws-render k8s-aws-validate terraform-fmt terraform-fmt-check terraform-init terraform-validate terraform-plan terraform-apply terraform-bootstrap-init terraform-bootstrap-validate terraform-bootstrap-plan terraform-bootstrap-apply clean

up:
	$(COMPOSE) up --build -d --wait --remove-orphans

down:
	$(COMPOSE) down --remove-orphans

build:
	$(COMPOSE) build

logs:
	$(COMPOSE) logs -f --tail=200

test:
	$(GO_DOCKER) go test -count=1 ./...

test-unit:
	$(GO_DOCKER) go test -short -count=1 ./...

test-integration:
	$(COMPOSE) --profile tools run --rm test-runner test -tags=integration -count=1 -timeout=2m ./tests/integration/...

test-e2e: up
	$(COMPOSE) --profile tools run --rm test-runner test -tags=e2e -count=1 -timeout=8m ./tests/e2e/...

migrate-up:
	$(COMPOSE) run --rm migrate -path=/migrations -database="$(DATABASE_URL)" up

migrate-down:
	$(COMPOSE) run --rm migrate -path=/migrations -database="$(DATABASE_URL)" down 1

lint:
	$(GO_DOCKER) go vet ./...

proto:
	docker run --rm -v "$(CURDIR):/workspace" -w /workspace bufbuild/buf:1.47.2 generate

config:
	$(COMPOSE) config

k8s-render:
	$(KUBECTL) kustomize $(KUSTOMIZE_OVERLAY)

k8s-apply:
	$(KUBECTL) apply -k $(KUSTOMIZE_OVERLAY)

k8s-delete:
	$(KUBECTL) delete -k $(KUSTOMIZE_OVERLAY)

k8s-aws-render:
	infra/terraform/scripts/render-aws-overlay.sh $(TF_ENV)

k8s-aws-validate: k8s-aws-render
	$(KUBECTL) kustomize deployments/kubernetes/overlays/aws/generated | docker run --rm -i ghcr.io/yannh/kubeconform:v0.7.0 -strict -summary -kubernetes-version 1.32.0 -skip ExternalSecret,ClusterSecretStore

terraform-fmt:
	docker run --rm -v "$(CURDIR):/workspace" -w /workspace $(TERRAFORM_IMAGE) fmt -recursive infra/terraform

terraform-fmt-check:
	docker run --rm -v "$(CURDIR):/workspace" -w /workspace $(TERRAFORM_IMAGE) fmt -recursive -check infra/terraform

terraform-init:
	test -f $(TF_DIR)/backend.hcl
	docker run --rm $(AWS_DOCKER_FLAGS) -v "$(CURDIR):/workspace" -w /workspace/$(TF_DIR) $(TERRAFORM_IMAGE) init -input=false -backend-config=backend.hcl

terraform-validate:
	docker volume create $(TERRAFORM_CACHE_VOLUME) >/dev/null
	docker run --rm --entrypoint /bin/sh \
		-e TF_DATA_DIR=/tmp/terraform-data \
		-e TF_PLUGIN_CACHE_DIR=/terraform-plugin-cache \
		-v "$(CURDIR):/workspace" \
		-v $(TERRAFORM_CACHE_VOLUME):/terraform-plugin-cache \
		-w /workspace/$(TF_DIR) \
		$(TERRAFORM_IMAGE) -ec 'terraform init -backend=false -input=false -lockfile=readonly && terraform validate'

terraform-plan:
	test -f $(TF_DIR)/terraform.tfvars
	docker run --rm $(AWS_DOCKER_FLAGS) -v "$(CURDIR):/workspace" -w /workspace/$(TF_DIR) $(TERRAFORM_IMAGE) plan -input=false -out=platform.tfplan

terraform-apply:
	test -f $(TF_DIR)/platform.tfplan
	docker run --rm $(AWS_DOCKER_FLAGS) -v "$(CURDIR):/workspace" -w /workspace/$(TF_DIR) $(TERRAFORM_IMAGE) apply -input=false platform.tfplan

terraform-bootstrap-init:
	docker run --rm -v "$(CURDIR):/workspace" -w /workspace/infra/terraform/bootstrap $(TERRAFORM_IMAGE) init -input=false

terraform-bootstrap-validate:
	docker volume create $(TERRAFORM_CACHE_VOLUME) >/dev/null
	docker run --rm --entrypoint /bin/sh \
		-e TF_DATA_DIR=/tmp/terraform-data \
		-e TF_PLUGIN_CACHE_DIR=/terraform-plugin-cache \
		-v "$(CURDIR):/workspace" \
		-v $(TERRAFORM_CACHE_VOLUME):/terraform-plugin-cache \
		-w /workspace/infra/terraform/bootstrap \
		$(TERRAFORM_IMAGE) -ec 'terraform init -backend=false -input=false -lockfile=readonly && terraform validate'

terraform-bootstrap-plan:
	test -f infra/terraform/bootstrap/terraform.tfvars
	docker run --rm $(AWS_DOCKER_FLAGS) -v "$(CURDIR):/workspace" -w /workspace/infra/terraform/bootstrap $(TERRAFORM_IMAGE) plan -input=false -out=bootstrap.tfplan

terraform-bootstrap-apply:
	test -f infra/terraform/bootstrap/bootstrap.tfplan
	docker run --rm $(AWS_DOCKER_FLAGS) -v "$(CURDIR):/workspace" -w /workspace/infra/terraform/bootstrap $(TERRAFORM_IMAGE) apply -input=false bootstrap.tfplan

clean:
	$(COMPOSE) --profile tools down --volumes --remove-orphans
