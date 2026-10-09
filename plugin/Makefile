NAME         ?= sub2api-iq
DIST         ?= dist
BIN          := $(DIST)/bin
# 发布包必须包含全部目标平台的运行时可执行文件：宿主安装时只接受声明了自身
# <goos>-<goarch> 运行时的包，缺失该平台的包会被判为「不支持当前运行平台」。
TARGETS      ?= linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64
HOST_TARGET  := $(shell go env GOOS)-$(shell go env GOARCH)
ALL_TARGETS  := $(sort $(TARGETS) $(HOST_TARGET))
S2PLUGIN     ?= go tool s2plugin
HOST_VERSION ?= 0.2.8
SIGNING_KEY  ?=
KEY_ID       ?=
PACKAGE      := $(DIST)/$(NAME).s2plugin

.PHONY: all test build pack verify run-info run-forward run-health keygen clean fmt lint

all: test build pack verify

# go vet + gofmt 检查 + 单元测试（含框架一致性套件）。
test:
	go vet ./...
	@fmtout="$$(gofmt -l .)"; if [ -n "$$fmtout" ]; then echo "以下文件未通过 gofmt:"; echo "$$fmtout"; exit 1; fi
	go test ./... -count=1

# 交叉编译所有目标平台的插件二进制。
build:
	@set -e; for t in $(ALL_TARGETS); do \
	  os=$${t%-*}; arch=$${t#*-}; ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	  echo "building $$t"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o $(BIN)/$$t/plugin$$ext ./cmd/plugin; \
	done

# 用框架打包器生成 .s2plugin（含文件哈希校验与可选 ed25519 签名）。
pack: build
	@mkdir -p $(DIST)
	@set -e; args=""; for t in $(ALL_TARGETS); do \
	  os=$${t%-*}; ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	  args="$$args -runtime $$t=$(BIN)/$$t/plugin$$ext"; \
	done; \
	sign=""; if [ -n "$(SIGNING_KEY)" ]; then sign="-signing-key $(SIGNING_KEY) -key-id $(KEY_ID)"; fi; \
	$(S2PLUGIN) pack -manifest manifest.source.json -ui ui -output $(PACKAGE) $$args $$sign

# 以宿主安装规则校验包。默认按本机平台校验，可用 PLATFORM 覆盖，
# 例如 `make verify PLATFORM=linux-amd64` 验证发布包覆盖了目标平台。
PLATFORM     ?= $(HOST_TARGET)

# 以宿主安装规则校验包（未签名时需宿主 allow_unsigned=true）。
verify:
	$(S2PLUGIN) verify -allow-unsigned -host-version $(HOST_VERSION) -platform $(PLATFORM) $(PACKAGE)

run-info:
	$(S2PLUGIN) run -package $(PACKAGE) info

run-health:
	$(S2PLUGIN) run -package $(PACKAGE) health

# 用法：make run-forward URL=https://api.openai.com/v1/models
run-forward:
	$(S2PLUGIN) run -package $(PACKAGE) forward -url "$(URL)"

# 生成发布者签名密钥；私钥绝不入库。
keygen:
	@mkdir -p build/keys
	$(S2PLUGIN) keygen -out build/keys/publisher

fmt:
	gofmt -w .

lint:
	golangci-lint run

clean:
	rm -rf $(DIST) build
