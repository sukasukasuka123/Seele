// micro_tool/example/main.go
// [TEST SKILL] echo —— 原样返回输入
// 用途：验证 microHub → agentfactory → Agent 整条调用链
package main

import (
	"encoding/json"
	"fmt"
	"log"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// EchoRequest 对应 registry.yaml input_schema
type EchoRequest struct {
	Content string `json:"content"`
}

// EchoResponse 对应 registry.yaml output_schema
type EchoResponse struct {
	Content string `json:"content"`
}

type EchoHandler struct{}

func (h *EchoHandler) ServiceName() string { return "echo" }

func (h *EchoHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	// 解析参数：[]byte → struct
	var params EchoRequest
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
	}
	if params.Content == "" {
		params.Content = "(empty)"
	}

	resp, err := tool.NewOKResp(h.ServiceName(), EchoResponse{
		Content: params.Content,
	})
	if err != nil {
		return nil, err
	}

	fmt.Printf("[echo] content=%q\n", params.Content)
	return []*pb.ToolResponse{resp}, nil
}

func main() {
	log.Println("[echo] TEST SKILL 启动，监听 :50101")
	if err := tool.New(&EchoHandler{}).Serve(":50101"); err != nil {
		log.Fatalf("%v", err)
	}
}
