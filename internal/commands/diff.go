package commands

import "fmt"

type diffOp struct {
	tag            byte // '=', '-', '+'
	i1, i2, j1, j2 int
}

// lineOps is an LCS diff of two line lists as runs of equal, deleted and inserted lines;
// deletions come before insertions, as in difflib.
func lineOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	add := func(tag byte, i, j int) {
		di, dj := 0, 0
		switch tag {
		case '=':
			di, dj = 1, 1
		case '-':
			di = 1
		case '+':
			dj = 1
		}
		if k := len(ops) - 1; k >= 0 && ops[k].tag == tag {
			ops[k].i2 += di
			ops[k].j2 += dj
			return
		}
		ops = append(ops, diffOp{tag, i, i + di, j, j + dj})
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			add('=', i, j)
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] > lcs[i+1][j]):
			add('+', i, j)
			j++
		default:
			add('-', i, j)
			i++
		}
	}
	return ops
}

func unifiedRange(start, stop int) string {
	begin, length := start+1, stop-start
	if length == 1 {
		return fmt.Sprint(begin)
	}
	if length == 0 {
		begin--
	}
	return fmt.Sprintf("%d,%d", begin, length)
}

// unifiedDiff renders a diff like Python's difflib.unified_diff(lineterm="") with 3 lines of
// context; it is empty when the inputs are equal.
func unifiedDiff(a, b []string, from, to string) []string {
	ops := lineOps(a, b)
	if len(ops) == 0 || (len(ops) == 1 && ops[0].tag == '=') {
		return nil
	}
	const ctx = 3
	// Trim the leading and trailing context, then split on equal runs longer than 2*ctx.
	if ops[0].tag == '=' {
		ops[0].i1 = max(ops[0].i1, ops[0].i2-ctx)
		ops[0].j1 = max(ops[0].j1, ops[0].j2-ctx)
	}
	if k := len(ops) - 1; ops[k].tag == '=' {
		ops[k].i2 = min(ops[k].i2, ops[k].i1+ctx)
		ops[k].j2 = min(ops[k].j2, ops[k].j1+ctx)
	}
	var groups [][]diffOp
	var cur []diffOp
	for _, op := range ops {
		if op.tag == '=' && op.i2-op.i1 > 2*ctx {
			cur = append(cur, diffOp{'=', op.i1, op.i1 + ctx, op.j1, op.j1 + ctx})
			groups = append(groups, cur)
			cur = []diffOp{{'=', op.i2 - ctx, op.i2, op.j2 - ctx, op.j2}}
			continue
		}
		cur = append(cur, op)
	}
	if len(cur) > 0 && !(len(cur) == 1 && cur[0].tag == '=') {
		groups = append(groups, cur)
	}
	out := []string{"--- " + from, "+++ " + to}
	for _, g := range groups {
		first, last := g[0], g[len(g)-1]
		out = append(out, fmt.Sprintf("@@ -%s +%s @@", unifiedRange(first.i1, last.i2), unifiedRange(first.j1, last.j2)))
		for _, op := range g {
			switch op.tag {
			case '=':
				for _, l := range a[op.i1:op.i2] {
					out = append(out, " "+l)
				}
			case '-':
				for _, l := range a[op.i1:op.i2] {
					out = append(out, "-"+l)
				}
			case '+':
				for _, l := range b[op.j1:op.j2] {
					out = append(out, "+"+l)
				}
			}
		}
	}
	return out
}
