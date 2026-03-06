package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── request / response ──────────────────────────────────────────────────────

type CodegenRequest struct {
	// skill 基本信息
	Name        string `json:"name"`        // skill 名称，如 "weather"
	Port        int    `json:"port"`        // 监听端口，如 50106
	Description string `json:"description"` // 给 LLM 看的描述（会写进 method 字段注释）

	// 参数结构描述（LLM 填写，codegen 据此生成 struct 和 schema）
	InputFields  []FieldDef `json:"input_fields"`  // 输入参数列表
	OutputFields []FieldDef `json:"output_fields"` // 输出参数列表

	// 核心逻辑描述（以 TODO 注释形式嵌入生成代码）
	LogicHints []string `json:"logic_hints"` // LLM 要实现的逻辑要点，每条一个 TODO
}

type FieldDef struct {
	Name     string `json:"name"`     // 字段名（小写下划线）
	Type     string `json:"type"`     // go 类型：string | int | bool | float64
	JsonTag  string `json:"json_tag"` // json tag，默认同 name
	Required bool   `json:"required"` // 是否必填
	Comment  string `json:"comment"`  // 字段注释
}

type CodegenResponse struct {
	Name     string `json:"name"`
	FilePath string `json:"file_path"` // 生成文件的路径
	Code     string `json:"code"`      // 生成的完整代码
	Error    string `json:"error,omitempty"`
}

// ── handler ─────────────────────────────────────────────────────────────────

type CodegenHandler struct {
	toolsDir string // tools 放置根目录，来自 .env TOOLS_DIR
}

func (h *CodegenHandler) ServiceName() string { return "codegen" }

func (h *CodegenHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	var p CodegenRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("参数解析失败: %v", err))
	}
	if p.Name == "" {
		return errResp(h.ServiceName(), "name 不能为空")
	}
	if p.Port == 0 {
		return errResp(h.ServiceName(), "port 不能为 0")
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

	code, err := generateCode(p)
	if err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("代码生成失败: %v", err))
	}

	// 写入文件
	skillDir := filepath.Join(h.toolsDir, p.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("创建目录失败: %v", err))
	}
	filePath := filepath.Join(skillDir, "main.go")
	if err := os.WriteFile(filePath, []byte(code), 0644); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("写入文件失败: %v", err))
	}

	resp, rerr := tool.NewOKResp(h.ServiceName(), CodegenResponse{
		Name:     p.Name,
		FilePath: filePath,
		Code:     code,
	})
	if rerr != nil {
		return nil, rerr
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── code generation ──────────────────────────────────────────────────────────

const skillTemplate = `package main

import (
	"encoding/json"
	"fmt"
	// TODO: 根据实现需要添加其他 import，例如 "net/http"、"os/exec" 等

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

func (h *{{.CapName}}Handler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	var p {{.CapName}}Request
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("参数解析失败: %v", err))
	}

	// TODO: 验证必填参数
	// 示例：if p.SomeField == "" { return errResp(h.ServiceName(), "some_field 不能为空") }
{{range .Required}}
	if p.{{.}} == "" {
		return errResp(h.ServiceName(), "{{toSnake .}} 不能为空")
	}
{{- end}}

	result, err := execute(p)
	if err != nil {
		result.Error = err.Error()
	}

	resp, rerr := tool.NewOKResp(h.ServiceName(), result)
	if rerr != nil {
		return nil, rerr
	}
	return []*pb.ToolResponse{resp}, nil
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

// ── helpers ──────────────────────────────────────────────────────────────────

func errResp(name, msg string) ([]*pb.ToolResponse, error) {
	resp, err := tool.NewOKResp(name, {{.CapName}}Response{Error: msg})
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	tool.New(&{{.CapName}}Handler{}).Serve(":{{.Port}}")
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

// capitalize 将 snake_case 转为 PascalCase
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

// toSnakeCase 将 PascalCase 转回 snake_case（用于错误提示）
func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteRune('_')
		}
		b.WriteRune(r | 32) // 转小写
	}
	return b.String()
}

// ── helpers ──────────────────────────────────────────────────────────────────

func errResp(name, msg string) ([]*pb.ToolResponse, error) {
	resp, err := tool.NewOKResp(name, CodegenResponse{Error: msg})
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	toolsDir := os.Getenv("TOOLS_DIR")
	if toolsDir == "" {
		toolsDir = "micro_tool"
	}
	tool.New(&CodegenHandler{toolsDir: toolsDir}).Serve(":50106")
}
