package core

import (
	"fmt"
	"strings"
)

// Log collects what one sync step found: ok lines, changes ("+ x", "- x", "~ x") and issues.
// Details keep full tool output behind an issue; they are not part of the JSON contract.
type Log struct {
	Title   string
	OK      []string
	Changes []string
	Issues  []string
	Details []string
	// Facts name what an issue or ok line is about, for scan; not part of the JSON contract.
	Facts []Fact
}

// Fact is one finding-shaped observation: a scan code and its subject.
type Fact struct{ Code, Subject string }

func NewLog(title string) *Log { return &Log{Title: title} }

func (l *Log) Ok(format string, a ...any) { l.OK = append(l.OK, fmt.Sprintf(format, a...)) }
func (l *Log) Change(format string, a ...any) {
	l.Changes = append(l.Changes, fmt.Sprintf(format, a...))
}
func (l *Log) Issue(format string, a ...any) { l.Issues = append(l.Issues, fmt.Sprintf(format, a...)) }

func (l *Log) Fact(code, subject string) { l.Facts = append(l.Facts, Fact{code, subject}) }

// Merge appends another log's lines.
func (l *Log) Merge(o *Log) {
	l.OK = append(l.OK, o.OK...)
	l.Changes = append(l.Changes, o.Changes...)
	l.Issues = append(l.Issues, o.Issues...)
	l.Details = append(l.Details, o.Details...)
	l.Facts = append(l.Facts, o.Facts...)
}

func (l *Log) ToObject() *Object {
	return Obj("title", strings.TrimSpace(l.Title), "ok", nonNil(l.OK), "changes", nonNil(l.Changes),
		"issues", nonNil(l.Issues))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
