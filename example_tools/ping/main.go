package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/sukasukasuka123/Seele/util"
	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

type PingRequest struct {
	Host    string `json:"host"`
	Count   int    `json:"count"`
	Timeout int    `json:"timeout"`
}

type PingResponse struct {
	Host       string  `json:"host"`
	Reachable  bool    `json:"reachable"`
	LatencyMs  float64 `json:"latency_ms"`
	PacketLoss float64 `json:"packet_loss"`
	Error      string  `json:"error,omitempty"`
}

type PingHandler struct{}

func (h *PingHandler) ServiceName() string { return "ping" }

func (h *PingHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var params PingRequest
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("ping: parse params: %w", err)
		}
	}
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

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		timeoutDur := time.Duration(params.Timeout) * time.Millisecond
		countStr := fmt.Sprintf("%d", params.Count)
		var args []string
		if runtime.GOOS == "windows" {
			args = []string{"ping", "-n", countStr, params.Host}
		} else {
			args = []string{"ping", "-c", countStr, "-W", "2", params.Host}
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeoutDur+time.Second)
		defer cancel()

		cmdResult, err := util.RunCmd(ctx, timeoutDur, args...)

		pr := PingResponse{
			Host:      params.Host,
			Reachable: err == nil && cmdResult.ExitCode == 0,
			LatencyMs: float64(cmdResult.Elapsed.Milliseconds()),
		}
		if err != nil {
			pr.Reachable = false
			pr.PacketLoss = 100
			pr.Error = err.Error()
		}

		resp, buildErr := pb_api.OKResp("ping", req.TaskId, pr)
		if buildErr != nil {
			ch <- pb_api.ErrorResp("ping", req.TaskId, "BUILD_RESP", buildErr.Error(), "")
			return
		}
		ch <- resp
		fmt.Printf("[ping] task=%s host=%s reachable=%v latency=%dms\n",
			req.TaskId, params.Host, pr.Reachable, cmdResult.Elapsed.Milliseconds())
	}()
	return ch, nil
}

func main() {
	log.Println("[ping] 启动，监听 :50102")
	if err := tool.New(&PingHandler{}).Serve(":50102"); err != nil {
		log.Fatalf("[ping] %v", err)
	}
}
