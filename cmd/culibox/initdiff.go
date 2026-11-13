package main

import "strings"

// diffContext is how many unchanged lines lineDiff keeps around each change.
const diffContext = 3

// lineDiff is a unified-style line diff of before and after: each line comes
// out with "-" (only in before), "+" (only in after) or " " (in both), from a
// longest common subsequence of the lines. Unchanged lines more than
// diffContext away from a change are left out, each run of them shown as one
// "..." line. Config files are small, so the quadratic table is fine.
func lineDiff(before, after []byte) string {
	a, b := diffLines(before), diffLines(after)
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
	type op struct {
		mark byte
		line string
	}
	ops := make([]op, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]: // a removal before an addition
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}

	keep := make([]bool, len(ops))
	for k, o := range ops {
		if o.mark == ' ' {
			continue
		}
		for x := max(0, k-diffContext); x <= min(len(ops)-1, k+diffContext); x++ {
			keep[x] = true
		}
	}
	var sb strings.Builder
	elided := false
	for k, o := range ops {
		if !keep[k] {
			if !elided {
				sb.WriteString("...\n")
				elided = true
			}
			continue
		}
		elided = false
		sb.WriteByte(o.mark)
		sb.WriteString(o.line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// diffLines splits b into lines without their newlines; a final newline does
// not make an empty last line.
func diffLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
