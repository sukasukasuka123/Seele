# ─────────────────────────────────────────────────────────────────────────────
# Seele — example_tools 管理 Makefile
#
# 用法速查：
#   make up          启动所有 skill 进程（后台）
#   make down        停止所有 skill 进程
#   make restart     重启所有 skill 进程
#   make status      查看各 skill 端口监听状态
#   make logs        实时跟踪所有 skill 日志（Ctrl-C 退出）
#   make run         启动所有 skill 后再启动主程序（前台）
#
#   单独操作：
#   make up-ping     只启动 ping
#   make down-ping   只停止 ping
#   make log-ping    只看 ping 的日志
# ─────────────────────────────────────────────────────────────────────────────

# ── 路径配置（与 .env 保持一致，也可被环境变量覆盖）─────────────────────────
PROJECT_ROOT  ?= $(shell pwd)
LOG_DIR       ?= $(PROJECT_ROOT)/.logs
PID_DIR       ?= $(PROJECT_ROOT)/.pids

# cmd skill 的默认工作目录
CMD_SKILL_DIR   ?= $(PROJECT_ROOT)
# registry skill 读取的 yaml 路径
REGISTRY_PATH   ?= $(PROJECT_ROOT)/config_example/registry.yaml
# codegen skill 生成代码的根目录
TOOLS_DIR       ?= $(PROJECT_ROOT)/example_tools

# ── skill 定义表（名称 : 相对路径 : 端口）────────────────────────────────────
#
# 格式：每行一个变量，SKILL_<NAME>_DIR 和 SKILL_<NAME>_PORT
# 新增 skill 只需在这里加两行，其余 target 自动生效。

SKILL_NAMES := suka_secret echo ping fetch cmd registry codegen

SKILL_suka_secret_DIR  := example_tools/suka_secret
SKILL_suka_secret_PORT := 50100

SKILL_echo_DIR         := example_tools/example
SKILL_echo_PORT        := 50101

SKILL_ping_DIR         := example_tools/ping
SKILL_ping_PORT        := 50102

SKILL_fetch_DIR        := example_tools/fetch
SKILL_fetch_PORT       := 50103

SKILL_cmd_DIR          := example_tools/cmd
SKILL_cmd_PORT         := 50104

SKILL_registry_DIR     := example_tools/registry_changer
SKILL_registry_PORT    := 50105

SKILL_codegen_DIR      := example_tools/tool_coder
SKILL_codegen_PORT     := 50106

# ── 工具检测 ──────────────────────────────────────────────────────────────────
GO    := $(shell command -v go    2>/dev/null)
LSOF  := $(shell command -v lsof  2>/dev/null)
SS    := $(shell command -v ss    2>/dev/null)

# 检测端口是否在监听（跨平台：优先 ss，其次 lsof，再次 netstat）
define port_listening
$(shell \
  if [ -n "$(SS)" ]; then \
    $(SS) -tlnp 2>/dev/null | grep -q ':$(1) ' && echo yes || echo no; \
  elif [ -n "$(LSOF)" ]; then \
    $(LSOF) -i :$(1) -sTCP:LISTEN -t 2>/dev/null | grep -q . && echo yes || echo no; \
  else \
    netstat -tlnp 2>/dev/null | grep -q ':$(1) ' && echo yes || echo no; \
  fi)
endef

# ── 内部工具宏 ────────────────────────────────────────────────────────────────

# 读取 pid 文件，返回 pid（文件不存在则空）
define read_pid
$(shell cat $(PID_DIR)/$(1).pid 2>/dev/null)
endef

# 启动单个 skill 的通用宏
# $(1) = skill 名称
define start_skill
	@mkdir -p $(LOG_DIR) $(PID_DIR)
	@pid=$(call read_pid,$(1)); \
	if [ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null; then \
		echo "  [skip]  $(1) 已在运行 (pid=$$pid)"; \
	else \
		echo "  [start] $(1) → :$(SKILL_$(1)_PORT)"; \
		env CMD_SKILL_DIR="$(CMD_SKILL_DIR)" \
		    REGISTRY_PATH="$(REGISTRY_PATH)" \
		    TOOLS_DIR="$(TOOLS_DIR)" \
		go run ./$(SKILL_$(1)_DIR) \
			> $(LOG_DIR)/$(1).log 2>&1 & \
		echo $$! > $(PID_DIR)/$(1).pid; \
	fi
endef

# 停止单个 skill 的通用宏
define stop_skill
	@pid=$(call read_pid,$(1)); \
	if [ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null; then \
		echo "  [stop]  $(1) (pid=$$pid)"; \
		kill "$$pid" && rm -f $(PID_DIR)/$(1).pid; \
	else \
		echo "  [skip]  $(1) 未运行"; \
		rm -f $(PID_DIR)/$(1).pid; \
	fi
endef

# ── 顶层 target ───────────────────────────────────────────────────────────────

.PHONY: up down restart status logs run clean help \
        $(addprefix up-,$(SKILL_NAMES)) \
        $(addprefix down-,$(SKILL_NAMES)) \
        $(addprefix log-,$(SKILL_NAMES))

## up: 启动所有 skill（已运行的跳过）
up:
	@echo "▶ 启动所有 skill..."
	$(foreach name,$(SKILL_NAMES),$(call start_skill,$(name)))
	@echo ""
	@echo "提示：'make status' 查看端口状态，'make logs' 查看实时日志"

## down: 停止所有 skill
down:
	@echo "▶ 停止所有 skill..."
	$(foreach name,$(SKILL_NAMES),$(call stop_skill,$(name)))
	@echo "✓ 全部已停止"

## restart: 先停止再启动所有 skill
restart: down
	@sleep 1
	@$(MAKE) up

## status: 查看各 skill 端口监听状态
status:
	@echo ""
	@printf "  %-18s %-8s %-12s %s\n" "SKILL" "PORT" "LISTENING" "PID"
	@printf "  %s\n" "──────────────────────────────────────────"
	$(foreach name,$(SKILL_NAMES), \
		@printf "  %-18s %-8s %-12s %s\n" \
			"$(name)" \
			":$(SKILL_$(name)_PORT)" \
			"$(call port_listening,$(SKILL_$(name)_PORT))" \
			"$(call read_pid,$(name))";)
	@echo ""

## logs: 实时跟踪所有 skill 日志（Ctrl-C 退出）
logs:
	@mkdir -p $(LOG_DIR)
	@echo "▶ 跟踪日志（Ctrl-C 退出）..."
	@echo "  日志目录: $(LOG_DIR)"
	@echo ""
	@tail -f $(foreach name,$(SKILL_NAMES),$(LOG_DIR)/$(name).log) 2>/dev/null \
		|| echo "  （暂无日志文件，请先 make up）"

## run: 启动所有 skill 后启动主程序（前台，Ctrl-C 停止主程序但 skill 继续运行）
run: up
	@echo ""
	@echo "▶ 等待 skill 端口就绪..."
	@$(MAKE) _wait_all
	@echo "▶ 启动主程序..."
	@echo ""
	go run ./cmd

## clean: 停止所有 skill 并清理 pid / 日志文件
clean: down
	@echo "▶ 清理 pid 和日志..."
	@rm -rf $(PID_DIR) $(LOG_DIR)
	@echo "✓ 清理完成"

## help: 显示帮助
help:
	@echo ""
	@echo "Seele example_tools Makefile"
	@echo ""
	@echo "  make up            启动所有 skill"
	@echo "  make down          停止所有 skill"
	@echo "  make restart       重启所有 skill"
	@echo "  make status        查看端口监听状态"
	@echo "  make logs          实时跟踪所有日志"
	@echo "  make run           启动 skill + 主程序"
	@echo "  make clean         停止并清理所有临时文件"
	@echo ""
	@echo "  单独操作（以 ping 为例）："
	@echo "  make up-ping       只启动 ping"
	@echo "  make down-ping     只停止 ping"
	@echo "  make log-ping      只看 ping 的日志"
	@echo ""
	@echo "  环境变量覆盖（示例）："
	@echo "  CMD_SKILL_DIR=/path make up"
	@echo "  REGISTRY_PATH=/path/registry.yaml make up"
	@echo ""

# ── 单 skill target（自动生成）────────────────────────────────────────────────

# up-<name>
define make_up_target
up-$(1):
	@echo "▶ 启动 $(1)..."
	$(call start_skill,$(1))
endef
$(foreach name,$(SKILL_NAMES),$(eval $(call make_up_target,$(name))))

# down-<name>
define make_down_target
down-$(1):
	@echo "▶ 停止 $(1)..."
	$(call stop_skill,$(1))
endef
$(foreach name,$(SKILL_NAMES),$(eval $(call make_down_target,$(name))))

# log-<name>
define make_log_target
log-$(1):
	@mkdir -p $(LOG_DIR)
	@echo "▶ $(1) 日志（Ctrl-C 退出）"
	@tail -f $(LOG_DIR)/$(1).log 2>/dev/null \
		|| echo "  （暂无日志，请先 make up-$(1)）"
endef
$(foreach name,$(SKILL_NAMES),$(eval $(call make_log_target,$(name))))

# ── 内部：等待所有端口就绪（供 run target 使用）──────────────────────────────

.PHONY: _wait_all
_wait_all:
	$(foreach name,$(SKILL_NAMES), \
		@$(MAKE) --no-print-directory _wait_port PORT=$(SKILL_$(name)_PORT) NAME=$(name);)

.PHONY: _wait_port
_wait_port:
	@retries=0; \
	while [ "$(call port_listening,$(PORT))" != "yes" ]; do \
		retries=$$((retries + 1)); \
		if [ $$retries -ge 20 ]; then \
			echo "  [warn] $(NAME) :$(PORT) 等待超时，继续..."; \
			break; \
		fi; \
		printf "  [wait] $(NAME) :$(PORT) (%d/20)\r" $$retries; \
		sleep 0.5; \
	done; \
	echo "  [ ok ] $(NAME) :$(PORT)                    "