package util

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CmdResult 命令执行结果
type CmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Elapsed  time.Duration
}

// RunCmd 执行命令，带超时，不走 shell（安全）
// args[0] 为命令名，其余为参数
func RunCmd(ctx context.Context, timeout time.Duration, args ...string) (*CmdResult, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("RunCmd: no command provided")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	result := &CmdResult{
		Stdout:   strings.TrimSpace(stdout.String()),
		Stderr:   strings.TrimSpace(stderr.String()),
		Elapsed:  elapsed,
		ExitCode: 0,
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil // 非零退出码由调用方判断
		}
		return nil, fmt.Errorf("cmd run: %w", err)
	}
	return result, nil
}

// MicroToolProcess 代表一个运行中的 micro_tool 子进程
type MicroToolProcess struct {
	Name string
	PID  int
	stop func()
}

// Stop 终止进程
func (p *MicroToolProcess) Stop() {
	if p.stop != nil {
		p.stop()
	}
}

// StartMicroTool 在后台启动一个 micro_tool（go run .）
// dir 为 micro_tool 的目录，extraArgs 为附加参数
// 返回进程句柄，调用 Stop() 终止
func StartMicroTool(ctx context.Context, name, dir string, extraArgs ...string) (*MicroToolProcess, error) {
	goArgs := append([]string{"run", "."}, extraArgs...)
	cmd := exec.CommandContext(ctx, "go", goArgs...)
	cmd.Dir = dir

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start micro_tool %s at %s: %w", name, dir, err)
	}

	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // 防止僵尸进程

	return &MicroToolProcess{
		Name: name,
		PID:  pid,
		stop: func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		},
	}, nil
}
