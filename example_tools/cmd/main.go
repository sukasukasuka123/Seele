package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// —— 请求/响应结构体（不变）——

type CmdRequest struct {
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Timeout int    `json:"timeout"`
}

type CmdResponse struct {
	Command  string `json:"command"`
	Dir      string `json:"dir"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Blocked  bool   `json:"blocked,omitempty"`
	Error    string `json:"error,omitempty"`
}

// —— 安全白名单（不变）——

var allowedPrefixes = []string{
	"go build", "go run", "go test", "go vet", "go mod", "go list",
	"go env", "go version", "go fmt",
	"cat ", "type ", "dir ", "ls ", "ls\n", "dir\n",
	"head ", "tail ", "more ", "less ",
	"find ", "where ", "which ",
	"echo ", "echo\n", "pwd", "cd ",
	"git status", "git log", "git diff", "git branch", "git show",
	"go doc",
}

var blockedPatterns = []string{
	"rm ", "rm\n", "rmdir", "del ", "del\n", "deltree", "format",
	"passwd", "password", "sudo", "su ", "su\n", "runas",
	"net user", "useradd", "usermod", "chown", "chmod",
	"curl ", "wget ", "invoke-webrequest", "start-bitstransfer",
	"apt ", "apt-get", "yum ", "brew ", "choco ", "winget",
	"pip ", "npm install", "yarn add",
	"reg add", "reg delete", "regedit", "regsvr",
	"sc create", "sc delete", "sc config",
	"netsh", "schtasks /create", "schtasks /delete",
	"powershell -enc", "powershell -e ", "cmd /c", "bash -c",
	"eval ", "exec(",
	"taskkill", "kill ", "killall", "pkill",
	"shutdown", "reboot", "restart",
	"system32", "windows\\system", "/etc/passwd", "/etc/shadow",
	"~/.ssh", ".ssh/",
}

func checkSecurity(cmd string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	for _, pattern := range blockedPatterns {
		if strings.Contains(lower, pattern) {
			return false, fmt.Sprintf("命令包含被禁止的操作: %q", pattern)
		}
	}
	for _, prefix := range allowedPrefixes {
		p := strings.TrimSuffix(prefix, "\n")
		if lower == p || strings.HasPrefix(lower, strings.TrimSuffix(prefix, " ")+" ") ||
			lower == strings.TrimSuffix(prefix, " ") {
			return true, ""
		}
	}
	return false, "命令不在允许列表中"
}

// —— Handler ——

type CmdHandler struct {
	defaultDir string
}

func (h *CmdHandler) ServiceName() string { return "cmd" }

func (h *CmdHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var p CmdRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("cmd: parse params: %w", err)
	}
	if p.Command == "" {
		return nil, fmt.Errorf("cmd: command 不能为空")
	}
	if p.Timeout <= 0 {
		p.Timeout = 30
	}
	if p.Timeout > 120 {
		p.Timeout = 120
	}

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		allowed, reason := checkSecurity(p.Command)
		if !allowed {
			resp, err := pb_api.OKResp("cmd", req.TaskId, CmdResponse{
				Command: p.Command,
				Blocked: true,
				Error:   reason,
			})
			if err != nil {
				ch <- pb_api.ErrorResp("cmd", req.TaskId, "BUILD_RESP", err.Error(), "")
				return
			}
			ch <- resp
			return
		}

		dir := p.Dir
		if dir == "" {
			dir = h.defaultDir
		}
		dir = filepath.Clean(dir)

		result, execErr := runCommand(p.Command, dir, time.Duration(p.Timeout)*time.Second)
		if execErr != nil {
			result.Error = execErr.Error()
		}

		resp, err := pb_api.OKResp("cmd", req.TaskId, result)
		if err != nil {
			ch <- pb_api.ErrorResp("cmd", req.TaskId, "BUILD_RESP", err.Error(), "")
			return
		}
		ch <- resp
		fmt.Printf("[cmd] task=%s command=%q exit=%d\n", req.TaskId, p.Command, result.ExitCode)
	}()
	return ch, nil
}

// —— 执行逻辑（不变）——

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
			runErr = nil
		}
	}

	return CmdResponse{
		Command:  command,
		Dir:      dir,
		Stdout:   truncate(stdout.String(), 8000),
		Stderr:   truncate(stderr.String(), 2000),
		ExitCode: exitCode,
	}, runErr
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "\n...[输出已截断]"
}

func main() {
	defaultDir, _ := os.Getwd()
	if d := os.Getenv("CMD_SKILL_DIR"); d != "" {
		defaultDir = d
	}
	log.Println("[cmd] 启动，监听 :50104")
	if err := tool.New(&CmdHandler{defaultDir: defaultDir}).Serve(":50104"); err != nil {
		log.Fatalf("[cmd] %v", err)
	}
}
