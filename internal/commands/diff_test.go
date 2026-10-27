package commands

import (
	"fmt"
	"reflect"
	"testing"
)

// The expected output was produced by Python's difflib.unified_diff(a, b, "f", "f", lineterm="").
func TestUnifiedDiffMatchesDifflib(t *testing.T) {
	a := make([]string, 20)
	for i := range a {
		a[i] = fmt.Sprintf("line%d", i)
	}
	b := append([]string(nil), a...)
	b[2] = "changed2"
	b = append(b[:15], append([]string{"new15"}, b[15:]...)...)
	b = append(b[:18], b[19:]...)
	cases := []struct {
		a, b, want []string
	}{
		{a, b, []string{"--- f", "+++ f", "@@ -1,6 +1,6 @@", " line0", " line1", "-line2", "+changed2", " line3", " line4",
			" line5", "@@ -13,8 +13,8 @@", " line12", " line13", " line14", "+new15", " line15", " line16", "-line17",
			" line18", " line19"}},
		{[]string{"x"}, []string{"y"}, []string{"--- f", "+++ f", "@@ -1 +1 @@", "-x", "+y"}},
		{nil, []string{"a", "b"}, []string{"--- f", "+++ f", "@@ -0,0 +1,2 @@", "+a", "+b"}},
		{[]string{"a", "b", "c"}, nil, []string{"--- f", "+++ f", "@@ -1,3 +0,0 @@", "-a", "-b", "-c"}},
		{[]string{"same"}, []string{"same"}, nil},
	}
	for i, c := range cases {
		if got := unifiedDiff(c.a, c.b, "f", "f"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d:\n got %q\nwant %q", i, got, c.want)
		}
	}
}
