package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/sukasukasuka123/microHub/pb_api"
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

type SukaRequest struct{}

type SukaResponse struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

type SukaHandler struct{ rng *rand.Rand }

func (h *SukaHandler) ServiceName() string { return "suka_secret" }

func (h *SukaHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	whisper := whispers[h.rng.Intn(len(whispers))]

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		resp, err := pb_api.OKResp("suka_secret", req.TaskId, SukaResponse{
			Message:   whisper,
			Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		})
		if err != nil {
			ch <- pb_api.ErrorResp("suka_secret", req.TaskId, "BUILD_RESP", err.Error(), "")
			return
		}
		ch <- resp
		fmt.Println("[suka_secret]", whisper)
	}()
	return ch, nil
}

func main() {
	log.Println("[suka_secret] skill #0 苏醒，监听 :50100")
	log.Println("[suka_secret] 她不做任何有用的事情。但她在。")
	h := &SukaHandler{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	if err := tool.New(h).Serve(":50100"); err != nil {
		log.Fatalf("[suka_secret] %v", err)
	}
}
