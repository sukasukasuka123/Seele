package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

type EchoRequest struct {
	Message string `json:"message"`
}

type EchoResponse struct {
	Echo string `json:"echo"`
}

type EchoHandler struct{}

func (h *EchoHandler) ServiceName() string { return "echo" }

func (h *EchoHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var p EchoRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("echo: parse params: %w", err)
	}

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		resp, err := pb_api.OKResp("echo", req.TaskId, EchoResponse{Echo: p.Message})
		if err != nil {
			ch <- pb_api.ErrorResp("echo", req.TaskId, "BUILD_RESP", err.Error(), "")
			return
		}
		ch <- resp
		fmt.Printf("[echo] task=%s message=%q\n", req.TaskId, p.Message)
	}()
	return ch, nil
}

func main() {
	log.Println("[echo] 启动，监听 :50101")
	if err := tool.New(&EchoHandler{}).Serve(":50101"); err != nil {
		log.Fatalf("[echo] %v", err)
	}
}
