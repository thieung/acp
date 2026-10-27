package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestScriptsUseNoHTMLSinks(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/app.js")
	if err != nil || len(b) == 0 {
		t.Fatal("static/app.js is missing")
	}
	sink := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function|setTimeout\(\s*["']`)
	if m := sink.Find(b); m != nil {
		t.Fatalf("app.js uses %s; render with DOM APIs and textContent", m)
	}
	html, _ := fs.ReadFile(staticFS, "static/index.html")
	if regexp.MustCompile(`<script>|<style>|\son[a-z]+=|style="`).Match(html) {
		t.Fatal("index.html has inline script, style or event handlers, which the CSP blocks")
	}
}

func TestEveryFontHasItsLicense(t *testing.T) {
	fonts, _ := fs.Glob(staticFS, "static/fonts/*.woff")
	if len(fonts) == 0 {
		t.Fatal("no fonts embedded")
	}
	for _, f := range fonts {
		family := strings.SplitN(strings.TrimPrefix(f, "static/fonts/"), ".", 2)[0]
		if _, err := fs.Stat(staticFS, "static/fonts/"+family+".OFL.txt"); err != nil {
			t.Errorf("%s has no %s.OFL.txt next to it", f, family)
		}
	}
}
