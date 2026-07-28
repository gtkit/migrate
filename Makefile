.PHONY: tool check install-tools tag release-patch release-minor release-major gittag delcommit

LINT_TARGETS ?= ./...

# ── 发版参数（可按库在命令行覆盖，如 make tag REMOTE=origin BUMP=minor）──
REMOTE       ?= gtkit        # 推送远端名
VERSION_FILE ?= version.go   # 版本常量所在文件（需含 const Version = "vX.Y.Z"）
BUMP         ?= patch        # patch / minor / major

tool: ## golangci-lint + gofumpt
	@ echo "▶️ golangci-lint run"
	golangci-lint run $(LINT_TARGETS)
	gofumpt -l -w .
	@ echo "✅ golangci-lint run"

check: ## 漏洞与安全扫描
	govulncheck ./...
	gosec ./...

install-tools: ## 一键安装开发/发版所需工具
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install mvdan.cc/gofumpt@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest

## 发版：BUMP=patch|minor|major（默认 patch）。发 v2+ 前 go.mod 的 module path 必须已带 /vN。
tag:
	@set -e; \
	if [ -n "$$(git status --porcelain)" ]; then \
		echo "✗ 工作区不干净，发版前请先提交或清理："; git status --short; exit 1; \
	fi; \
	echo "▶️ go vet"; go vet ./...; \
	echo "▶️ 测试 (race)"; go test -race -count=1 -timeout=5m ./...; \
	current=$$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' $(VERSION_FILE) | head -n1 | tr -d 'v'); \
	if [ -z "$$current" ]; then echo "✗ 在 $(VERSION_FILE) 中找不到 const Version"; exit 1; fi; \
	maj=$$(echo $$current | cut -d. -f1); \
	min=$$(echo $$current | cut -d. -f2); \
	patch=$$(echo $$current | cut -d. -f3); \
	case "$(BUMP)" in \
	  patch) new="v$$maj.$$min.$$((patch+1))" ;; \
	  minor) new="v$$maj.$$((min+1)).0" ;; \
	  major) new="v$$((maj+1)).0.0" ;; \
	  *) echo "✗ BUMP 必须为 patch / minor / major（当前: $(BUMP)）"; exit 1 ;; \
	esac; \
	newmaj=$$(echo "$$new" | sed -E 's/^v([0-9]+).*/\1/'); \
	if [ "$$newmaj" -ge 2 ]; then \
	  modpath=$$(awk '/^module /{print $$2; exit}' go.mod); \
	  case "$$modpath" in \
	    */v$$newmaj) : ;; \
	    *) echo "✗ 目标 $$new 属 v$$newmaj，但 go.mod module path（$$modpath）未带 /v$$newmaj 后缀。"; \
	       echo "  Go module 规范：v2+ 必须先把 module path 改为 .../v$$newmaj 并同步内部 import，再发版；"; \
	       echo "  仅打 tag 而不改 module path 是错误发布。已中止。"; \
	       exit 1 ;; \
	  esac; \
	fi; \
	printf "Bump (%s): v%s -> %s\n" "$(BUMP)" "$$current" "$$new"; \
	sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$$new"'\3/' $(VERSION_FILE); \
	rm -f $(VERSION_FILE).bak; \
	git add $(VERSION_FILE); \
	git commit -m "chore(release): $$new"; \
	git tag -a "$$new" -m "release $$new"; \
	git push $(REMOTE) HEAD; \
	git push $(REMOTE) "$$new"; \
	printf "Done: %s\n" "$$new"

release-patch: ## 发布 PATCH（bug 修复 / 文档 / 内部重构）
	@$(MAKE) tag BUMP=patch

release-minor: ## 发布 MINOR（向后兼容新增导出 API / Option）
	@$(MAKE) tag BUMP=minor

release-major: ## 发布 MAJOR（破坏性变更；需 go.mod module path 已带 /vN）
	@$(MAKE) tag BUMP=major

gittag: ## 显示最新 tag
	git tag --sort=-version:refname | head -1

delcommit: ## 撤销最近一次提交，保留改动
	git reset --soft HEAD~1
