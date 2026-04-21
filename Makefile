.PHONY: build clean install test run help

# 变量
BINARY_NAME=knowly
BUILD_DIR=bin
CMD_DIR=cmd/knowly
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME) -s -w"

help:
	@echo "可用命令:"
	@echo "  make build     - 构建所有平台的二进制文件"
	@echo "  make darwin    - 仅构建 macOS 版本"
	@echo "  make linux     - 仅构建 Linux 版本"
	@echo "  make clean     - 清理构建文件"
	@echo "  make install   - 安装依赖和构建"
	@echo "  make test      - 运行测试"
	@echo "  make run       - 运行程序"

build:
	@echo "构建所有平台..."
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/knowly-darwin ./$(CMD_DIR)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/knowly-darwin-arm64 ./$(CMD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/knowly-linux ./$(CMD_DIR)
	@chmod +x $(BUILD_DIR)/*
	@echo "✓ 构建完成"

darwin:
	@echo "构建 macOS 版本..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/knowly-darwin ./$(CMD_DIR)
	@chmod +x $(BUILD_DIR)/knowly-darwin
	@echo "✓ macOS 版本构建完成"

linux:
	@echo "构建 Linux 版本..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/knowly-linux ./$(CMD_DIR)
	@chmod +x $(BUILD_DIR)/knowly-linux
	@echo "✓ Linux 版本构建完成"

clean:
	@echo "清理构建文件..."
	@rm -rf $(BUILD_DIR)
	@echo "✓ 清理完成"

install:
	@echo "安装依赖..."
	npm install
	@echo "构建二进制文件..."
	npm run build
	@echo "✓ 安装完成"

test:
	@echo "运行测试..."
	go test -v ./...

run:
	@echo "运行程序..."
	go run ./$(CMD_DIR) --help
