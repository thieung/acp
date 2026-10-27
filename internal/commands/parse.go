package commands

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type flagKind int

const (
	boolFlag flagKind = iota
	strFlag
	intFlag
	listFlag // repeatable string
)

type flagSpec struct {
	name    string
	short   string
	kind    flagKind
	def     string
	choices []string
	help    string
	meta    string
}

type posSpec struct {
	name     string
	many     bool // takes every remaining positional (nargs="*")
	optional bool
	choices  []string
	def      string
	help     string
}

type command struct {
	name   string
	help   string
	pos    []posSpec
	flags  []flagSpec
	run    handler
	action bool // the JSON kind includes the action positional
	server bool // long-running; --json does not wrap it
}

// Args is one parsed command line.
type Args struct {
	cmd   *command
	json  bool
	bools map[string]bool
	strs  map[string]string
	lists map[string][]string
	given map[string]bool
}

func (a *Args) Bool(name string) bool     { return a.bools[name] }
func (a *Args) Str(name string) string    { return a.strs[name] }
func (a *Args) List(name string) []string { return a.lists[name] }
func (a *Args) Given(name string) bool    { return a.given[name] }

func (a *Args) Int(name string) int {
	n, _ := strconv.Atoi(a.strs[name])
	return n
}

// Kind is the envelope kind: acp.<command>, plus the action for skills, kit, setup and hooks.
func (a *Args) Kind() string {
	kind := "acp." + a.cmd.name
	if a.cmd.action && a.strs["action"] != "" {
		kind += "." + a.strs["action"]
	}
	return kind
}

var errHelp = errors.New("help requested")

// errVersion asks Main to print the version.
var errVersion = errors.New("version requested")

type parseError struct {
	cmd *command
	msg string
}

func (p *parseError) Error() string { return p.msg }

var jsonFlag = flagSpec{name: "json", kind: boolFlag, help: "print {schema_version, kind, data}"}

// parse reads global flags, the command name, then the command's flags and positionals in any
// order (argparse parity). A bare `acp` is `acp status`.
func parse(argv []string) (*Args, error) {
	a := &Args{bools: map[string]bool{}, strs: map[string]string{}, lists: map[string][]string{},
		given: map[string]bool{}}
	i := 0
	for ; i < len(argv); i++ {
		tok := argv[i]
		switch {
		case tok == "--json":
			a.json = true
		case tok == "--version":
			return a, errVersion
		case tok == "-h" || tok == "--help":
			return a, errHelp
		case strings.HasPrefix(tok, "-") && tok != "-":
			return a, &parseError{msg: fmt.Sprintf("unrecognized arguments: %s", tok)}
		default:
			goto found
		}
	}
found:
	name := "status"
	if i < len(argv) {
		name = argv[i]
		i++
	}
	cmd := lookup(name)
	if cmd == nil {
		return a, &parseError{msg: fmt.Sprintf("argument command: invalid choice: '%s' (choose from %s)", name,
			quoteList(commandNames()))}
	}
	a.cmd = cmd
	flags := append([]flagSpec{jsonFlag}, cmd.flags...)
	for _, f := range flags {
		switch f.kind {
		case strFlag, intFlag:
			if f.def != "" {
				a.strs[f.name] = f.def
			}
		case listFlag:
			a.lists[f.name] = nil
		}
	}
	var positionals []string
	help := false
	onlyPos := false
	for ; i < len(argv); i++ {
		tok := argv[i]
		if onlyPos || tok == "-" || !strings.HasPrefix(tok, "-") || isNumber(tok) {
			positionals = append(positionals, tok)
			continue
		}
		if tok == "--" {
			onlyPos = true
			continue
		}
		if tok == "-h" || tok == "--help" {
			help = true
			continue
		}
		key, value, hasValue := strings.Cut(tok, "=")
		f := findFlag(flags, key)
		if f == nil {
			return a, &parseError{cmd, fmt.Sprintf("unrecognized arguments: %s", tok)}
		}
		if f.kind == boolFlag {
			if hasValue {
				return a, &parseError{cmd, fmt.Sprintf("argument --%s: ignored explicit argument '%s'", f.name, value)}
			}
			a.bools[f.name] = true
			if f.name == "json" {
				a.json = true
			}
			continue
		}
		if !hasValue {
			if i+1 >= len(argv) {
				return a, &parseError{cmd, fmt.Sprintf("argument --%s: expected one argument", f.name)}
			}
			i++
			value = argv[i]
		}
		if f.kind == intFlag {
			if _, err := strconv.Atoi(value); err != nil {
				return a, &parseError{cmd, fmt.Sprintf("argument --%s: invalid int value: '%s'", f.name, value)}
			}
		}
		if len(f.choices) > 0 && !contains(f.choices, value) {
			return a, &parseError{cmd, fmt.Sprintf("argument --%s: invalid choice: '%s' (choose from %s)", f.name,
				value, quoteList(f.choices))}
		}
		a.given[f.name] = true
		if f.kind == listFlag {
			a.lists[f.name] = append(a.lists[f.name], value)
		} else {
			a.strs[f.name] = value
		}
	}
	if err := a.assign(positionals, help); err != nil {
		return a, err
	}
	if help {
		return a, errHelp
	}
	return a, nil
}

func (a *Args) assign(values []string, help bool) error {
	cmd := a.cmd
	required := 0
	for _, p := range cmd.pos {
		if !p.optional && !p.many {
			required++
		}
	}
	for _, p := range cmd.pos {
		switch {
		case p.many:
			a.lists[p.name] = append([]string{}, values...)
			values = nil
		case len(values) > 0 && (!p.optional || len(values) > required):
			v := values[0]
			if len(p.choices) > 0 && !contains(p.choices, v) {
				return &parseError{cmd, fmt.Sprintf("argument %s: invalid choice: '%s' (choose from %s)", p.name, v,
					quoteList(p.choices))}
			}
			a.strs[p.name] = v
			values = values[1:]
			if !p.optional {
				required--
			}
		case !p.optional:
			if help {
				return nil
			}
			missing := []string{}
			for _, q := range cmd.pos {
				if !q.optional && !q.many && a.strs[q.name] == "" {
					missing = append(missing, q.name)
				}
			}
			return &parseError{cmd, "the following arguments are required: " + strings.Join(missing, ", ")}
		default:
			a.strs[p.name] = p.def
		}
	}
	if len(values) > 0 && !help {
		return &parseError{cmd, "unrecognized arguments: " + strings.Join(values, " ")}
	}
	return nil
}

func findFlag(flags []flagSpec, key string) *flagSpec {
	for i := range flags {
		f := &flags[i]
		if key == "--"+f.name || (f.short != "" && key == "-"+f.short) {
			return f
		}
	}
	return nil
}

func isNumber(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }

func contains(list []string, s string) bool {
	for _, it := range list {
		if it == s {
			return true
		}
	}
	return false
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = "'" + it + "'"
	}
	return strings.Join(q, ", ")
}

func usageLine(cmd *command) string {
	if cmd == nil {
		return "usage: acp [-h] [--json] [--version] {" + strings.Join(commandNames(), ",") + "} ..."
	}
	parts := []string{"usage: acp " + cmd.name, "[-h]", "[--json]"}
	for _, f := range cmd.flags {
		flag := "--" + f.name
		if f.short != "" {
			flag = "-" + f.short
		}
		if f.kind != boolFlag {
			flag += " " + metaOf(f)
		}
		parts = append(parts, "["+flag+"]")
	}
	for _, p := range cmd.pos {
		switch {
		case p.many:
			parts = append(parts, "["+p.name+" ...]")
		case p.optional:
			parts = append(parts, "["+posName(p)+"]")
		default:
			parts = append(parts, posName(p))
		}
	}
	return strings.Join(parts, " ")
}

func posName(p posSpec) string {
	if len(p.choices) > 0 {
		return "{" + strings.Join(p.choices, ",") + "}"
	}
	return p.name
}

func metaOf(f flagSpec) string {
	if f.meta != "" {
		return f.meta
	}
	if len(f.choices) > 0 {
		return "{" + strings.Join(f.choices, ",") + "}"
	}
	return strings.ToUpper(strings.ReplaceAll(f.name, "-", "_"))
}

func printHelp(w io.Writer, cmd *command) {
	if cmd == nil {
		fmt.Fprintln(w, usageLine(nil))
		fmt.Fprint(w, "\n"+overview)
		return
	}
	fmt.Fprintln(w, usageLine(cmd))
	fmt.Fprintf(w, "\n%s\n", cmd.help)
	if len(cmd.pos) > 0 {
		fmt.Fprintln(w, "\npositional arguments:")
		for _, p := range cmd.pos {
			fmt.Fprintf(w, "  %-22s %s\n", posName(p), p.help)
		}
	}
	fmt.Fprintln(w, "\noptions:")
	fmt.Fprintf(w, "  %-22s %s\n", "-h, --help", "show this help message and exit")
	for _, f := range append([]flagSpec{jsonFlag}, cmd.flags...) {
		name := "--" + f.name
		if f.short != "" {
			name = "-" + f.short + ", " + name
		}
		if f.kind != boolFlag {
			name += " " + metaOf(f)
		}
		fmt.Fprintf(w, "  %-22s %s\n", name, f.help)
	}
}
