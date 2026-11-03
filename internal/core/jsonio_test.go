package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOrderedJSONRoundTripKeepsKeyOrder(t *testing.T) {
	in := "{\n  \"id\": \"demo\",\n  \"agentkit\": {\n    \"kit\": \"engineer\",\n    \"targets\": [\n      \"claude-code\"\n    ]\n  },\n  \"skills\": [],\n  \"n\": 1.50,\n  \"html\": \"<a&b>\"\n}\n"
	v, err := ParseJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip changed the document:\n%s", out)
	}
	obj := v.(*Object)
	obj.Set("skills", []any{"x"})
	obj.Set("new", true)
	obj.Delete("n")
	if got := strings.Join(obj.Keys(), ","); got != "id,agentkit,skills,html,new" {
		t.Fatalf("keys = %s", got)
	}
}

func TestReadJSONErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadJSON(filepath.Join(dir, "missing.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{\n  \"a\": 1,\n  bad\n}"), 0o644)
	_, err = ReadJSON(bad)
	if err == nil || !strings.Contains(err.Error(), bad) || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("malformed file error should name the file and line: %v", err)
	}
}

func TestWriteJSONKeepsModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared", "settings.json")
	os.MkdirAll(filepath.Dir(target), 0o755)
	os.WriteFile(target, []byte("{}"), 0o600)
	link := filepath.Join(dir, "instance-settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	obj := NewObject()
	obj.Set("hooks", NewObject())
	if err := WriteJSON(link, obj, 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a file")
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(target)
	if string(data) != "{\n  \"hooks\": {}\n}\n" {
		t.Fatalf("content = %q", data)
	}
	fresh := filepath.Join(dir, "new", "file.json")
	if err := WriteJSON(fresh, NewObject(), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(fresh); fi.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode = %v", fi.Mode().Perm())
	}
}

func TestWriteJSONParallelWritesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := NewObject()
			o.Set("i", float64(i))
			errs <- WriteJSON(filepath.Join(dir, "f.json"), o, 0o644)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %d entries", len(entries))
	}
	if _, err := ReadJSON(filepath.Join(dir, "f.json")); err != nil {
		t.Fatal(err)
	}
}
