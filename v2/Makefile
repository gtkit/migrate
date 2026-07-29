.PHONY: lint check tag gittag

LINT_TARGETS ?= ./...
# BUMP 控制发版号递增级别：patch | minor（本项目永不发 v3，不提供 major）.
BUMP ?= patch
# VULNCHECK=off 显式跳过漏洞扫描（仅限 vuln.go.dev 不可达的环境，默认强制）.
VULNCHECK ?= on
# INTEGRATION=off 显式跳过真实数据库集成测试（默认强制：需设置
# MIGRATE_TEST_MYSQL_DSN 与 MIGRATE_TEST_POSTGRES_DSN 指向可丢弃的测试库）.
INTEGRATION ?= on
# 发版覆盖率门槛（百分比，统计口径含集成测试）.
COVER_MIN ?= 80

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
	ver=$${new#v}; \
	if ! grep -q '^## \[Unreleased\]' ../CHANGELOG.md; then \
		echo "✗ CHANGELOG.md 缺少 [Unreleased] 区段"; exit 1; \
	fi; \
	if ! grep -q "^## \[$$ver\]" ../CHANGELOG.md; then \
		echo "✗ CHANGELOG.md 缺少 [$$ver] 版本区段：发版前先把 [Unreleased] 内容剪切到 [$$ver]"; exit 1; \
	fi; \
	echo "▶️ go vet"; go vet ./...; \
	echo "▶️ golangci-lint"; golangci-lint run ./...; \
	echo "▶️ go mod tidy -diff"; go mod tidy -diff; \
	if [ "$(INTEGRATION)" = "off" ]; then \
		echo "⚠️ INTEGRATION=off：已跳过真实数据库集成测试"; testtags=""; \
	elif [ -z "$$MIGRATE_TEST_MYSQL_DSN" ] || [ -z "$$MIGRATE_TEST_POSTGRES_DSN" ]; then \
		echo "✗ 缺少 MIGRATE_TEST_MYSQL_DSN / MIGRATE_TEST_POSTGRES_DSN（真实数据库集成测试是发版门禁；或显式 INTEGRATION=off 跳过）"; exit 1; \
	else \
		testtags="-tags=integration"; \
	fi; \
	echo "▶️ 测试 (race$${testtags:+ + integration})"; go test $$testtags -race -count=1 -timeout=10m ./...; \
	echo "▶️ 覆盖率 (>= $(COVER_MIN)%)"; go test $$testtags -count=1 -coverprofile=coverage.out ./...; \
	pct=$$(go tool cover -func=coverage.out | awk '/^total:/{gsub(/%/,"",$$NF); print $$NF}'); \
	rm -f coverage.out; \
	echo "覆盖率: $$pct%"; \
	if ! awk -v p="$$pct" -v m="$(COVER_MIN)" 'BEGIN{exit !(p+0 >= m+0)}'; then \
		echo "✗ 覆盖率 $$pct% 低于门槛 $(COVER_MIN)%"; exit 1; \
	fi; \
	echo "▶️ benchmark"; go test -run='^$$' -bench=. -benchmem -count=3 ./...; \
	if [ "$(VULNCHECK)" = "off" ]; then \
		echo "⚠️ VULNCHECK=off：已跳过 govulncheck（仅限 vuln.go.dev 不可达的环境）"; \
	else \
		echo "▶️ govulncheck"; govulncheck ./...; \
	fi; \
	printf "Bump(%s): v%s -> %s\n" "$(BUMP)" "$$current" "$$new"; \
	sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$$new"'\3/' version.go; \
	rm -f version.go.bak; \
	git add version.go; \
	git commit -m "chore(release): 发布 $$new"; \
	{ printf "版本 %s\n\n主要变更：\n" "$$new"; \
	  awk -v ver="$$ver" '$$0 ~ ("^## \\[" ver "\\]"){f=1;next} /^## \[/{f=0} f' ../CHANGELOG.md; } > .tagmsg; \
	git tag -a "$$new" --cleanup=whitespace -F .tagmsg; \
	rm -f .tagmsg; \
	printf "Tag: %s\n" "$$new"; \
	git push --atomic gtkit HEAD "refs/tags/$$new"; \
	printf "Done\n"

gittag: ## Show latest tag
	git tag --sort=-version:refname | grep '^v2\.' | head -1