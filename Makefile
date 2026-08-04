SPEC := api/openapi/bugsnag-data-access-api.yaml
SPEC_URL := https://api.swaggerhub.com/apis/smartbear-public/bugsnag-data-access-api/2/swagger.yaml

# Pinned so CI and a developer lint against the same version. Kept in step with
# the version in our other Go repos.
LINT_VERSION := 2.11.4
LINT := ./bin/golangci-lint

.PHONY: all build test lint fmt generate verify-codegen update-spec clean

all: build

# The version is read from the build info at runtime, so no ldflags are needed.
build:
	go build ./cmd/bugsnag

# JUNIT, when set, writes a JUnit report to that path. CI sets it, so CI and a
# developer run the same command with the same flags.
test:
	go tool gotestsum $(if $(JUNIT),--junitfile $(JUNIT),) -- -race -shuffle=on -count=1 ./...

# govet and staticcheck run as part of this, so there is no separate target for
# either. JUNIT behaves as it does for test.
lint: $(LINT)
	$(LINT) run $(if $(JUNIT),--output.text.path stdout --output.junit-xml.path $(JUNIT),)

# Applies the import grouping and gofmt rules that lint reports on.
fmt: $(LINT)
	$(LINT) fmt

# Rebuilt whenever the pinned version here changes.
$(LINT): Makefile
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b ./bin v$(LINT_VERSION)

generate:
	go generate ./...

# The whole defence against hand-edits to the generated client: regenerate and
# fail if the result differs from what is committed.
verify-codegen: generate
	git diff --exit-code -- internal/bugsnagapi

# Refresh the vendored spec from upstream. Vendored byte-for-byte so the diff
# is reviewable; regenerating afterwards will fail loudly if the refresh made
# any overlay action a no-op, since the overlay is applied with strict: true.
update-spec:
	curl -fsSL -o $(SPEC) '$(SPEC_URL)'
	$(MAKE) generate

clean:
	rm -rf bugsnag bin
