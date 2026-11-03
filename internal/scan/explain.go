package scan

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"

	"github.com/thieung/acp/internal/core"
)

//go:embed catalog_vi.json
var catalogJSON []byte

type entry struct {
	Title    string   `json:"title"`
	What     string   `json:"what"`
	Why      string   `json:"why"`
	Ignore   string   `json:"ignore"`
	Todo     string   `json:"todo"`
	Glossary []string `json:"glossary"`
}

type catalogFile struct {
	Codes    map[string]entry  `json:"codes"`
	Glossary map[string]string `json:"glossary"`
}

var catalog = func() catalogFile {
	var c catalogFile
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("scan: catalog_vi.json: " + err.Error())
	}
	return c
}()

// Explanation is one finding code in plain Vietnamese: what happened, why it matters, what happens
// if it is ignored and what to do, plus the terms it uses.
type Explanation struct {
	Code, Title, What, Why, Ignore, Todo string
	Glossary                             []Term
}

type Term struct{ Term, Meaning string }

// Object is the explanation as `acp explain --json` data.
func (e Explanation) Object() *core.Object {
	terms := []any{}
	for _, t := range e.Glossary {
		terms = append(terms, core.Obj("term", t.Term, "meaning", t.Meaning))
	}
	return core.Obj("code", e.Code, "title", e.Title, "what", e.What, "why", e.Why, "ignore", e.Ignore,
		"todo", e.Todo, "glossary", terms)
}

var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// fill replaces {param} with its value, or with <param> when the value is unknown.
func fill(text string, params map[string]string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		key := m[1 : len(m)-1]
		if v, ok := params[key]; ok && v != "" {
			return v
		}
		return "<" + key + ">"
	})
}

// Explain renders a code's explanation with a finding's params (nil for the generic text).
func Explain(code string, params map[string]string) (Explanation, bool) {
	e, ok := catalog.Codes[code]
	if !ok {
		return Explanation{}, false
	}
	out := Explanation{Code: code, Title: fill(e.Title, params), What: fill(e.What, params), Why: fill(e.Why, params),
		Ignore: fill(e.Ignore, params), Todo: fill(e.Todo, params)}
	for _, t := range e.Glossary {
		out.Glossary = append(out.Glossary, Term{t, catalog.Glossary[t]})
	}
	return out, true
}

// Glossary is every term with its meaning, sorted.
func Glossary() []Term {
	out := make([]Term, 0, len(catalog.Glossary))
	for t, m := range catalog.Glossary {
		out = append(out, Term{t, m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Term < out[j].Term })
	return out
}
