package claude

import "strings"

// diffContext is the number of unchanged context lines kept around each change
// group in a generated apply_patch hunk (matches the unified-diff default).
const diffContext = 3

type diffOp struct {
	kind byte // ' ' equal, '-' delete, '+' add
	text string
}

// diffOps computes a line-level edit script between oldLines and newLines
// using an LCS DP (delete-first on ties).
func diffOps(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{' ', oldLines[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', oldLines[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', newLines[j]})
	}
	return ops
}

func splitForDiff(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// unifiedHunks renders apply_patch-style hunk lines from a line diff of oldText
// vs newText: one "@@" per change group, context lines prefixed " ", deletions
// "-", additions "+". It also returns the added/removed line counts. When
// oldText is empty the whole body is an addition (used for Write / Add File).
func unifiedHunks(oldText, newText string) ([]string, int, int) {
	ops := diffOps(splitForDiff(oldText), splitForDiff(newText))
	added, removed := 0, 0
	changed := []int{}
	for i, o := range ops {
		switch o.kind {
		case '+':
			added++
		case '-':
			removed++
		}
		if o.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil, 0, 0
	}
	var lines []string
	for i := 0; i < len(changed); {
		start, end := changed[i], changed[i]
		j := i + 1
		// Merge adjacent change groups separated by <= 2*context equal lines so
		// their context does not overlap into two hunks.
		for j < len(changed) && changed[j]-end-1 <= 2*diffContext {
			end = changed[j]
			j++
		}
		lo := max(0, start-diffContext)
		hi := min(len(ops), end+1+diffContext)
		lines = append(lines, "@@")
		for _, o := range ops[lo:hi] {
			lines = append(lines, string(o.kind)+o.text)
		}
		i = j
	}
	return lines, added, removed
}
