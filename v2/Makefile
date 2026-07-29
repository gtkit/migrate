.PHONY: lint check tag gittag

LINT_TARGETS ?= ./...
# BUMP 控制发版号递增级别：patch | minor（本项目永不发 v3，不提供 major）.
BUMP ?= patch
# VULNCHECK=off 显式跳过漏洞扫描（仅限 vuln.go.dev 不可达的环境，默认强制）.
VULNCHECK ?= on

lint: ## Lint Go code
	@ echo "▶️ golangci-lint run"
	golangci-lint run $(LINT_TARGETS)
	gofumpt -l -w .
	@ echo "✅ golangci-lint run"

check: ## Check vulnerabilities
	govulncheck ./...
	gosec ./...

tag: ## Bump version (BUMP=patch|minor) and push tag
	@set -e; \
	case "$(BUMP)" in \
		patch|minor) ;; \
		*) echo "✗ BUMP 只支持 patch|minor（本项目永不发 v3）：$(BUMP)"; exit 1;; \
	esac; \
	if [ -n "$$(git status --porcelain)" ]; then \
		echo "✗ 工作区不干净，发版前请先提交或清理："; git status --short; exit 1; \
	fi; \
	echo "▶️ go vet"; go vet ./...; \
	echo "▶️ golangci-lint"; golangci-lint run ./...; \
	echo "▶️ 测试 (race)"; go test -race -count=1 -timeout=5m ./...; \
	if [ "$(VULNCHECK)" = "off" ]; then \
		echo "⚠️ VULNCHECK=off：已跳过 govulncheck（仅限 vuln.go.dev 不可达的环境）"; \
	else \
		echo "▶️ govulncheck"; govulncheck ./...; \
	fi; \
	if ! grep -q '^## \[Unreleased\]' ../CHANGELOG.md; then \
		echo "✗ CHANGELOG.md 缺少 [Unreleased] 区段"; exit 1; \
	fi; \
	current=$$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' version.go | head -n1 | tr -d 'v'); \
	if [ -z "$$current" ]; then echo "version not found in version.go"; exit 1; fi; \
	maj=$$(echo $$current | cut -d. -f1); \
	min=$$(echo $$current | cut -d. -f2); \
	patch=$$(echo $$current | cut -d. -f3); \
	if [ "$(BUMP)" = "minor" ]; then \
		new="v$$maj.$$(expr $$min + 1).0"; \
	else \
		new="v$$maj.$$min.$$(expr $$patch + 1)"; \
	fi; \
	printf "Bump(%s): v%s -> %s\n" "$(BUMP)" "$$current" "$$new"; \
	sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$$new"'\3/' version.go; \
	git add version.go; \
	git commit -m "chore(release): v2 $$new"; \
	printf "Release: %s\n" "$$new"; \
	git push gtkit HEAD; \
	git tag -a "$${new}" -m "release $$new"; \
	printf "Tag: %s\n" "$$new"; \
	git push gtkit "$${new}"; \
	printf "Done\n"
	rm -f version.go.bak

gittag: ## Show latest tag
	git tag --sort=-version:refname | grep '^v2\.' | head -1