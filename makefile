# ── 路径配置 ─────────────────────────────────────────────────────
TOOLS_DIR   := ./example_tools
CMD_MAIN    := ./cmd/main.go
BUILD_DIR   := ./bin

# 所有工具子目录（与 tree 输出一致）
TOOL_DIRS := \
	cmd \
	example \
	fetch \
	ping \
	registry_changer \
	suka_secret \
	tool_coder

# ── 构建 ──────────────────────────────────────────────────────────
.PHONY: build build-tools build-main

build: build-tools build-main

build-tools:
	@for dir in $(TOOL_DIRS); do \
		echo "[build] tool: $$dir"; \
		go build -o $(BUILD_DIR)/tools/$$dir$(EXT) $(TOOLS_DIR)/$$dir/main.go; \
	done

build-main:
	@echo "[build] main"
	@go build -o $(BUILD_DIR)/seele$(EXT) $(CMD_MAIN)

# ── 启动（Windows 用 run-win，Linux/Mac 用 run）─────────────────
.PHONY: run run-win

# Linux / Mac
run: build
	@echo "[start] launching tools..."
	@for dir in $(TOOL_DIRS); do \
		$(BUILD_DIR)/tools/$$dir & \
	done
	@echo "[start] waiting for tools to register (2s)..."
	@sleep 2
	@echo "[start] launching main..."
	@$(BUILD_DIR)/seele

# Windows（调用下方 start.ps1）
run-win: build
	@powershell -ExecutionPolicy Bypass -File ./scripts/start.ps1

# ── 清理 ──────────────────────────────────────────────────────────
.PHONY: clean
clean:
	@rm -rf $(BUILD_DIR)