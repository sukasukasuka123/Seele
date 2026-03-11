package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── request / response ──────────────────────────────────────────────────────

type CodegenRequest struct {
	Name         string     `json:"name"`
	Port         int        `json:"port"`
	Description  string     `json:"description"`
	InputFields  []FieldDef `json:"input_fields"`
	OutputFields []FieldDef `json:"output_fields"`
	LogicHints   []string   `json:"logic_hints"`
}

type FieldDef struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	JsonTag  string `json:"json_tag"`
	Required bool   `json:"required"`
	Comment  string `json:"comment"`
}

type CodegenResponse struct {
	Name     string `json:"name"`
	FilePath string `json:"file_path"`
	Code     string `json:"code"`
	Error    string `json:"error,omitempty"`
}

// ── handler ─────────────────────────────────────────────────────────────────

type CodegenHandler struct {
	toolsDir string
}

func (h *CodegenHandler) ServiceName() string { return "codegen" }

func (h *CodegenHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var p CodegenRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("codegen: parse params: %w", err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("codegen: name 不能为空")
	}
	if p.Port == 0 {
		return nil, fmt.Errorf("codegen: port 不能为 0")
	}

	// 补全字段默认值
	for i := range p.InputFields {
		if p.InputFields[i].JsonTag == "" {
			p.InputFields[i].JsonTag = p.InputFields[i].Name
		}
	}
	for i := range p.OutputFields {
		if p.OutputFields[i].JsonTag == "" {
			p.OutputFields[i].JsonTag = p.OutputFields[i].Name
		}
	}

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		code, err := generateCode(p)
		if err != nil {
			ch <- pb_api.ErrorResp("codegen", req.TaskId, "CODEGEN", err.Error(), "")
			return
		}

		skillDir := filepath.Join(h.toolsDir, p.Name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			ch <- pb_api.ErrorResp("codegen", req.TaskId, "MKDIR", err.Error(), "")
			return
		}
		filePath := filepath.Join(skillDir, "main.go")
		if err := os.WriteFile(filePath, []byte(code), 0644); err != nil {
			ch <- pb_api.ErrorResp("codegen", req.TaskId, "WRITE", err.Error(), "")
			return
		}

		resp, err := pb_api.OKResp("codegen", req.TaskId, CodegenResponse{
			Name:     p.Name,
			FilePath: filePath,
			Code:     code,
		})
		if err != nil {
			ch <- pb_api.ErrorResp("codegen", req.TaskId, "BUILD_RESP", err.Error(), "")
			return
		}
		ch <- resp
		fmt.Printf("[codegen] task=%s name=%s file=%s\n", req.TaskId, p.Name, filePath)
	}()
	return ch, nil
}

// ── code generation ──────────────────────────────────────────────────────────

// 模板生成的 tool 代码已同步改为新版 microHub API：
//   - Execute 签名：(<-chan *pb.ToolResponse, error)
//   - pb_api.OKResp / pb_api.ErrorResp 替代 tool.NewOKResp
const skillTemplate = `package main

import (
	"encoding/json"
	"fmt"
	"log"
	// TODO: 根据实现需要添加其他 import，例如 "net/http"、"os/exec" 等

	"github.com/sukasukasuka123/microHub/pb_api"
	pb   "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── request / response ──────────────────────────────────────────────────────

// {{.CapName}}Request 输入参数
type {{.CapName}}Request struct {
{{- range .Input}}
	{{.GoName}} {{.Type}} ` + "`" + `json:"{{.JsonTag}}"` + "`" + ` // {{.Comment}}
{{- end}}
}

// {{.CapName}}Response 输出结果
type {{.CapName}}Response struct {
{{- range .Output}}
	{{.GoName}} {{.Type}} ` + "`" + `json:"{{.JsonTag}}"` + "`" + ` // {{.Comment}}
{{- end}}
	Error string ` + "`" + `json:"error,omitempty"` + "`" + `
}

// ── handler ─────────────────────────────────────────────────────────────────

type {{.CapName}}Handler struct{}

func (h *{{.CapName}}Handler) ServiceName() string { return "{{.Name}}" }

func (h *{{.CapName}}Handler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var p {{.CapName}}Request
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("{{.Name}}: parse params: %w", err)
	}
{{range .Required}}
	if p.{{.}} == "" {
		return nil, fmt.Errorf("{{.Name}}: {{toSnake .}} 不能为空")
	}
{{- end}}

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		result, err := execute(p)
		if err != nil {
			result.Error = err.Error()
		}

		resp, rerr := pb_api.OKResp("{{.Name}}", req.TaskId, result)
		if rerr != nil {
			ch <- pb_api.ErrorResp("{{.Name}}", req.TaskId, "BUILD_RESP", rerr.Error(), "")
			return
		}
		ch <- resp
	}()
	return ch, nil
}

// ── core logic ───────────────────────────────────────────────────────────────

// execute 实现 {{.Name}} 的核心逻辑
//
// 描述：{{.Description}}
//
// 逻辑要点：
{{- range .LogicHints}}
// TODO: {{.}}
{{- end}}
func execute(p {{.CapName}}Request) ({{.CapName}}Response, error) {
	// TODO: 在此实现核心逻辑
	return {{.CapName}}Response{}, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	log.Println("[{{.Name}}] 启动，监听 :{{.Port}}")
	if err := tool.New(&{{.CapName}}Handler{}).Serve(":{{.Port}}"); err != nil {
		log.Fatalf("[{{.Name}}] %v", err)
	}
}
`

type templateData struct {
	Name        string
	CapName     string
	Description string
	Port        int
	Input       []fieldData
	Output      []fieldData
	Required    []string
	LogicHints  []string
}

type fieldData struct {
	GoName  string
	Type    string
	JsonTag string
	Comment string
}

func generateCode(p CodegenRequest) (string, error) {
	capName := capitalize(p.Name)

	var required []string
	var inputFields []fieldData
	for _, f := range p.InputFields {
		inputFields = append(inputFields, fieldData{
			GoName:  capitalize(f.Name),
			Type:    f.Type,
			JsonTag: f.JsonTag,
			Comment: f.Comment,
		})
		if f.Required && f.Type == "string" {
			required = append(required, capitalize(f.Name))
		}
	}

	var outputFields []fieldData
	for _, f := range p.OutputFields {
		outputFields = append(outputFields, fieldData{
			GoName:  capitalize(f.Name),
			Type:    f.Type,
			JsonTag: f.JsonTag,
			Comment: f.Comment,
		})
	}

	data := templateData{
		Name:        p.Name,
		CapName:     capName,
		Description: p.Description,
		Port:        p.Port,
		Input:       inputFields,
		Output:      outputFields,
		Required:    required,
		LogicHints:  p.LogicHints,
	}

	funcMap := template.FuncMap{
		"toSnake": toSnakeCase,
	}

	tmpl, err := template.New("skill").Funcs(funcMap).Parse(skillTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ── string helpers ───────────────────────────────────────────────────────────

func capitalize(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteRune('_')
		}
		b.WriteRune(r | 32)
	}
	return b.String()
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	toolsDir := os.Getenv("TOOLS_DIR")
	if toolsDir == "" {
		toolsDir = "micro_tool"
	}
	log.Println("[codegen] 启动，监听 :50106")
	if err := tool.New(&CodegenHandler{toolsDir: toolsDir}).Serve(":50106"); err != nil {
		log.Fatalf("[codegen] %v", err)
	}
}
