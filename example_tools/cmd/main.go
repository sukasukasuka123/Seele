package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── request / response ──────────────────────────────────────────────────────

type CmdRequest struct {
	Command string `json:"command"` // 要执行的命令
	Dir     string `json:"dir"`     // 工作目录，默认当前目录
	Timeout int    `json:"timeout"` // 超时秒数，默认 30，最大 120
}

type CmdResponse struct {
	Command  string `json:"command"`
	Dir      string `json:"dir"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Blocked  bool   `json:"blocked,omitempty"` // true = 被安全规则拦截
	Error    string `json:"error,omitempty"`
}

// ── security rules ───────────────────────────────────────────────────────────

// 白名单：允许的命令前缀（小写匹配）
// 只允许只读/编译/查看类操作
var allowedPrefixes = []string{
	// Go 工具链
	"go build", "go run", "go test", "go vet", "go mod", "go list",
	"go env", "go version", "go fmt",
	// 文件查看（只读）
	"cat ", "type ", "dir ", "ls ", "ls\n", "dir\n",
	"head ", "tail ", "more ", "less ",
	"find ", "where ", "which ",
	// 查看类系统信息
	"echo ", "echo\n", "pwd", "cd ",
	"git status", "git log", "git diff", "git branch", "git show",
	"go doc",
}

// 黑名单：拒绝包含这些词的命令（小写匹配）
// 优先级高于白名单
var blockedPatterns = []string{
	// 文件破坏
	"rm ", "rm\n", "rmdir", "del ", "del\n", "deltree", "format",
	// 权限/账户
	"passwd", "password", "sudo", "su ", "su\n", "runas",
	"net user", "useradd", "usermod", "chown", "chmod",
	// 网络下载
	"curl ", "wget ", "invoke-webrequest", "start-bitstransfer",
	"aria2", "axel", "httpie",
	// 包管理/安装
	"apt ", "apt-get", "yum ", "brew ", "choco ", "winget",
	"pip ", "npm install", "yarn add",
	// 注册表/系统配置
	"reg add", "reg delete", "regedit", "regsvr",
	"sc create", "sc delete", "sc config",
	"netsh", "schtasks /create", "schtasks /delete",
	// Shell 绕过
	"powershell -enc", "powershell -e ", "cmd /c", "bash -c",
	"eval ", "exec(",
	// 进程/服务
	"taskkill", "kill ", "killall", "pkill",
	"shutdown", "reboot", "restart",
	// 敏感路径操作
	"system32", "windows\\system", "/etc/passwd", "/etc/shadow",
	"~/.ssh", ".ssh/",
}

func checkSecurity(cmd string) (allowed bool, reason string) {
	lower := strings.ToLower(strings.TrimSpace(cmd))

	// 黑名单优先
	for _, pattern := range blockedPatterns {
		if strings.Contains(lower, pattern) {
			return false, fmt.Sprintf("命令包含被禁止的操作: %q", pattern)
		}
	}

	// 白名单检查
	for _, prefix := range allowedPrefixes {
		p := strings.TrimSuffix(prefix, "\n") // 处理单独命令（如 "ls\n" → "ls"）
		if lower == p || strings.HasPrefix(lower, strings.TrimSuffix(prefix, " ")+" ") ||
			lower == strings.TrimSuffix(prefix, " ") {
			return true, ""
		}
	}

	return false, "命令不在允许列表中。允许的操作：go 工具链、文件查看（cat/dir/ls）、git 查看类命令"
}

// ── handler ─────────────────────────────────────────────────────────────────

type CmdHandler struct {
	defaultDir string
}

func (h *CmdHandler) ServiceName() string { return "cmd" }

func (h *CmdHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	var p CmdRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("参数解析失败: %v", err))
	}
	if p.Command == "" {
		return errResp(h.ServiceName(), "command 不能为空")
	}
	if p.Timeout <= 0 {
		p.Timeout = 30
	}
	if p.Timeout > 120 {
		p.Timeout = 120
	}

	// 安全检查
	allowed, reason := checkSecurity(p.Command)
	if !allowed {
		resp, err := tool.NewOKResp(h.ServiceName(), CmdResponse{
			Command: p.Command,
			Blocked: true,
			Error:   reason,
		})
		if err != nil {
			return nil, err
		}
		return []*pb.ToolResponse{resp}, nil
	}

	// 工作目录
	dir := p.Dir
	if dir == "" {
		dir = h.defaultDir
	}
	dir = filepath.Clean(dir)

	result, execErr := runCommand(p.Command, dir, time.Duration(p.Timeout)*time.Second)
	if execErr != nil {
		result.Error = execErr.Error()
	}

	resp, err := tool.NewOKResp(h.ServiceName(), result)
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── exec logic ───────────────────────────────────────────────────────────────

func runCommand(command, dir string, timeout time.Duration) (CmdResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			runErr = nil // 非零退出码不是框架错误，返回给 LLM 判断
		}
	}

	// 截断超长输出
	const limit = 8000
	outStr := truncate(stdout.String(), limit)
	errStr := truncate(stderr.String(), 2000)

	return CmdResponse{
		Command:  command,
		Dir:      dir,
		Stdout:   outStr,
		Stderr:   errStr,
		ExitCode: exitCode,
	}, runErr
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + "\n...[输出已截断]"
}

// ── helpers ──────────────────────────────────────────────────────────────────

func errResp(name, msg string) ([]*pb.ToolResponse, error) {
	resp, err := tool.NewOKResp(name, CmdResponse{Error: msg})
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	defaultDir, _ := os.Getwd()
	if d := os.Getenv("CMD_SKILL_DIR"); d != "" {
		defaultDir = d
	}
	tool.New(&CmdHandler{defaultDir: defaultDir}).Serve(":50104")
}
