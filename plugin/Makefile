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
# 版本号从清单解析，使本地产物名与 CI 产物名保持一致（如 0.0.1）。
VERSION      := $(shell sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' manifest.source.json | head -1)
# 签名私钥路径；留空则构建未签名包（仅能在 plugins.allow_unsigned=true 的宿主安装）。
SIGNING_KEY  ?=
# 签名 key_id，必须与宿主 plugins.trusted_publishers 中的键一致。
KEY_ID       ?= sub2api-iq-v1
PACKAGE      := $(DIST)/$(NAME)-$(VERSION)-universal.s2plugin
# 以宿主安装规则校验包时使用的平台，默认本机平台。
PLATFORM     ?= $(HOST_TARGET)

.PHONY: all test build pack pack-platform verify run-info run-forward run-health keygen clean fmt lint

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

# 生成含全部平台的通用包：宿主在任何平台都能安装。
# 用法：make pack SIGNING_KEY=build/keys/publisher.private KEY_ID=my-key-v1
pack: build
	@mkdir -p $(DIST)
	@set -e; args=""; for t in $(ALL_TARGETS); do \
	  os=$${t%-*}; ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	  args="$$args -runtime $$t=$(BIN)/$$t/plugin$$ext"; \
	done; \
	sign=""; if [ -n "$(SIGNING_KEY)" ]; then sign="-signing-key $(SIGNING_KEY) -key-id $(KEY_ID)"; fi; \
	echo "version=$(VERSION) signed=$$([ -n "$(SIGNING_KEY)" ] && echo true || echo false)"; \
	$(S2PLUGIN) pack -manifest manifest.source.json -ui ui -output $(PACKAGE) $$args $$sign

# 只打包单个平台，产物体积约为通用包的五分之一。
# 用法：make pack-platform PLATFORM=linux-amd64
pack-platform:
	@set -e; t=$(PLATFORM); os=$${t%-*}; arch=$${t#*-}; ext=""; \
	  if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o $(BIN)/$$t/plugin$$ext ./cmd/plugin; \
	  sign=""; if [ -n "$(SIGNING_KEY)" ]; then sign="-signing-key $(SIGNING_KEY) -key-id $(KEY_ID)"; fi; \
	  echo "packing $$t (version=$(VERSION))"; \
	  $(S2PLUGIN) pack -manifest manifest.source.json -ui ui \
	    -output $(DIST)/$(NAME)-$(VERSION)-$$t.s2plugin \
	    -runtime $$t=$(BIN)/$$t/plugin$$ext $$sign

# 以宿主安装规则校验包。已签名时按受信任发布者校验（公钥由私钥推导：
# Ed25519 私钥 = 32 字节种子 || 32 字节公钥，因此无需单独存放公钥）；
# 未签名时需宿主 allow_unsigned=true。
verify:
	@if [ -n "$(SIGNING_KEY)" ]; then \
	  pub="$$(base64 -d $(SIGNING_KEY) | tail -c 32 | base64 -w0)"; \
	  $(S2PLUGIN) verify -host-version $(HOST_VERSION) -platform $(PLATFORM) -trusted "$(KEY_ID)=$$pub" $(PACKAGE); \
	else \
	  $(S2PLUGIN) verify -allow-unsigned -host-version $(HOST_VERSION) -platform $(PLATFORM) $(PACKAGE); \
	fi

run-info:
	$(S2PLUGIN) run -package $(PACKAGE) info

run-health:
	$(S2PLUGIN) run -package $(PACKAGE) health

# 用法：make run-forward URL=https://api.openai.com/v1/models
run-forward:
	$(S2PLUGIN) run -package $(PACKAGE) forward -url "$(URL)"

# 生成发布者签名密钥；私钥绝不入库。生成后把打印出的公钥写入宿主
# config.yaml 的 plugins.trusted_publishers，键名与 KEY_ID 一致。
keygen:
	@mkdir -p build/keys
	$(S2PLUGIN) keygen -out build/keys/publisher

fmt:
	gofmt -w .

lint:
	golangci-lint run

clean:
	rm -rf $(DIST) build
