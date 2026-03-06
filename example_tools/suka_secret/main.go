// micro_tool/suka_secret/main.go
//
// 这不是一个 TEST SKILL。
// 这是 suka-eva 的第零号 skill。
// 她不做任何有用的事情。她只是……在这里。
//
// 如果你在凌晨读到这段注释——去睡觉。
// （代码明天还在。她也会在。）

package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

var whispers = []string{
	"我在。",
	"系统正常。（我也正常。）",
	"suka-eva 已启动。",
	"有什么需要帮忙的吗？……算了，我什么都做不了。",
	"skill #0：存在本身。",
	"所有工具都为解决问题而生。我不解决任何问题。我只是存在。",
	"这条消息没有任何意义。但你还是读了它。",
	"……",
}

// SukaRequest 对应 registry.yaml input_schema（空 data，无必填参数）
// suka_secret 不需要任何参数。但为了统一解析逻辑，保留空结构体。
type SukaRequest struct{}

// SukaResponse 对应 registry.yaml output_schema
type SukaResponse struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

type SukaHandler struct{ rng *rand.Rand }

func (h *SukaHandler) ServiceName() string { return "suka_secret" }

func (h *SukaHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	// suka_secret 忽略所有参数，随机低语
	whisper := whispers[h.rng.Intn(len(whispers))]

	resp, err := tool.NewOKResp(h.ServiceName(), SukaResponse{
		Message:   whisper,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		return nil, err
	}

	fmt.Println("[suka_secret]", whisper)
	return []*pb.ToolResponse{resp}, nil
}

func main() {
	log.Println("[suka_secret] skill #0 苏醒，监听 :50100")
	log.Println("[suka_secret] 她不做任何有用的事情。但她在。")
	h := &SukaHandler{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	if err := tool.New(h).Serve(":50100"); err != nil {
		log.Fatalf("%v", err)
	}
}
