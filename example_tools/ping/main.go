// micro_tool/ping/main.go
// [TEST SKILL] ping —— 测试网络连通性
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/sukasukasuka123/Seele/util"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// PingRequest 对应 registry.yaml input_schema
type PingRequest struct {
	Host    string `json:"host"`
	Count   int    `json:"count"`
	Timeout int    `json:"timeout"` // ms
}

// PingResponse 对应 registry.yaml output_schema
type PingResponse struct {
	Host       string  `json:"host"`
	Reachable  bool    `json:"reachable"`
	LatencyMs  float64 `json:"latency_ms"`
	PacketLoss float64 `json:"packet_loss"`
	Error      string  `json:"error,omitempty"`
}

type PingHandler struct{}

func (h *PingHandler) ServiceName() string { return "ping" }

func (h *PingHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	// 解析参数：[]byte → struct
	var params PingRequest
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
	}
	// 默认值（与 registry.yaml schema default 一致）
	if params.Host == "" {
		params.Host = "127.0.0.1"
	}
	if params.Count <= 0 {
		params.Count = 4
	}
	if params.Count > 10 {
		params.Count = 10
	}
	if params.Timeout <= 0 {
		params.Timeout = 3000
	}

	timeoutSec := time.Duration(params.Timeout) * time.Millisecond

	// 构造 ping 命令（跨平台）
	countStr := fmt.Sprintf("%d", params.Count)
	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"ping", "-n", countStr, params.Host}
	} else {
		args = []string{"ping", "-c", countStr, "-W", "2", params.Host}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutSec+time.Second)
	defer cancel()

	cmdResult, err := util.RunCmd(ctx, timeoutSec, args...)

	pr := PingResponse{
		Host:       params.Host,
		Reachable:  err == nil && cmdResult.ExitCode == 0,
		LatencyMs:  float64(cmdResult.Elapsed.Milliseconds()),
		PacketLoss: 0,
	}
	if err != nil {
		pr.Reachable = false
		pr.PacketLoss = 100
		pr.Error = err.Error()
	}

	resp, buildErr := tool.NewOKResp(h.ServiceName(), pr)
	if buildErr != nil {
		return nil, buildErr
	}

	fmt.Printf("[ping] host=%s reachable=%v latency=%dms\n",
		params.Host, pr.Reachable, cmdResult.Elapsed.Milliseconds())
	return []*pb.ToolResponse{resp}, nil
}

func main() {
	log.Println("[ping] TEST SKILL 启动，监听 :50102")
	if err := tool.New(&PingHandler{}).Serve(":50102"); err != nil {
		log.Fatalf("%v", err)
	}
}
