.PHONY: lint check tag gittag sync-v2

GO ?= go
PYTHON ?= python
LINT_TARGETS ?= ./...
V2_VERSION ?=

lint:
	@echo "▶️  golangci-lint run"
	golangci-lint run $(LINT_TARGETS)
	gofumpt -l -w .
	@echo "✅ golangci-lint run"

check:
	govulncheck $(LINT_TARGETS)

sync-v2:
	@if [ -n "$(V2_VERSION)" ]; then \
		$(PYTHON) tools/sync_v2.py --repo . --version $(V2_VERSION); \
	else \
		$(PYTHON) tools/sync_v2.py --repo .; \
	fi

tag:
	@current=$$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' version.go | head -n1 | tr -d 'v'); \
	if [ -z "$$current" ]; then echo "version not found in version.go"; exit 1; fi; \
	maj=$$(echo $$current | cut -d. -f1); \
	min=$$(echo $$current | cut -d. -f2); \
	patch=$$(echo $$current | cut -d. -f3); \
	newpatch=$$(expr $$patch + 1); \
	new="v$$maj.$$min.$$newpatch"; \
	printf "Bump: v%s -> %s\n" "$$current" "$$new"; \
	sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$$new"'\3/' version.go; \
	git add version.go; \
	git commit -m "chore(release): $$new"; \
	printf "Release: %s\n" "$$new"; \
	git push gtkit HEAD; \
	git tag -a "$$new" -m "release $$new"; \
	printf "Tag: %s\n" "$$new"; \
	git push gtkit "$$new"; \
	printf "Done\n"
	rm -f version.go.bak

gittag:
	git tag --sort=-version:refname | head -1
