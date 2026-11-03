package commands

import (
	"context"
	"fmt"

	"github.com/thieung/acp/internal/scan"
)

var severityLabel = map[string]string{
	scan.Error: "lỗi", scan.Warn: "cần sửa", scan.Decide: "bạn quyết định", scan.Info: "để ý",
}

var severityColor = map[string]string{scan.Error: "31", scan.Warn: "33", scan.Decide: "35", scan.Info: "2"}

var moduleLabel = map[string]string{
	"projects": "Dự án & Skill", "agentkit": "AgentKit & Runtime", "hooks": "Hook, MCP & Cài đặt",
}

func cmdScan(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	r := scan.Run(context.Background(), root, scan.Options{Module: a.Str("module")})
	report := r.Object()
	for _, k := range report.Keys() {
		v, _ := report.Get(k)
		e.Set(k, v)
	}
	for _, m := range scan.Modules {
		s, ok := r.Summary[m]
		if !ok {
			continue
		}
		state := e.Paint("32", "ổn")
		if s["total"] > 0 {
			state = e.Paint("33", fmt.Sprintf("%d mục", s["total"]))
		}
		e.Printf("%s  %s\n", e.Paint("1", moduleLabel[m]), state)
		for _, f := range r.Findings {
			if f.Module != m {
				continue
			}
			ex, _ := scan.Explain(f.Code, f.Params)
			e.Printf("  %s %s  %s\n", e.Paint(severityColor[f.Severity], "["+severityLabel[f.Severity]+"]"), ex.Title,
				e.Paint("2", f.Code))
			e.Printf("    %s\n", ex.Todo)
			if f.Fix != nil {
				e.Printf("    %s %s\n", e.Paint("2", "xem trước:"), f.Fix.Preview())
			}
		}
	}
	if len(r.Findings) == 0 {
		e.Printf("%s\n", e.Paint("32", "Không có gì cần sửa."))
	} else {
		e.Printf("%s\n", e.Paint("2", "chi tiết một mục: acp explain <code>"))
	}
	return 0, nil
}

func cmdExplain(e *Env, a *Args) (int, error) {
	code := a.Str("code")
	ex, ok := scan.Explain(code, nil)
	if !ok {
		return 0, fail("unknown finding code %q; `acp scan` shows the codes it found", code)
	}
	o := ex.Object()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		e.Set(k, v)
	}
	e.Printf("%s\n\n", e.Paint("1", ex.Title))
	for _, part := range [][2]string{{"Chuyện gì", ex.What}, {"Vì sao quan trọng", ex.Why}, {"Nếu bỏ qua", ex.Ignore}, {"Nên làm gì", ex.Todo}} {
		e.Printf("%s\n  %s\n", e.Paint("1", part[0]), part[1])
	}
	if len(ex.Glossary) > 0 {
		e.Printf("\n%s\n", e.Paint("1", "Thuật ngữ"))
		for _, t := range ex.Glossary {
			e.Printf("  %s: %s\n", t.Term, t.Meaning)
		}
	}
	return 0, nil
}
