// Package coverprofile parses Go coverage profiles written by go test -coverprofile.
package coverprofile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Block struct {
	StartLine, StartCol int
	EndLine, EndCol     int
	NumStmt, Count      int
}

// Profile maps a file's import path (as written by go test) to its blocks.
type Profile map[string][]Block

func Parse(r io.Reader) (Profile, error) {
	p := Profile{}
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return nil, errors.New("coverprofile: missing mode line")
			}
			continue
		}
		file, b, err := parseLine(line)
		if err != nil {
			return nil, err
		}
		p[file] = append(p[file], b)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for file, blocks := range p {
		p[file] = merge(blocks)
	}
	return p, nil
}

func parseLine(line string) (string, Block, error) {
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return "", Block{}, fmt.Errorf("coverprofile: malformed line %q", line)
	}
	var b Block
	_, err := fmt.Sscanf(line[i+1:], "%d.%d,%d.%d %d %d",
		&b.StartLine, &b.StartCol, &b.EndLine, &b.EndCol, &b.NumStmt, &b.Count)
	if err != nil {
		return "", Block{}, fmt.Errorf("coverprofile: malformed line %q: %w", line, err)
	}
	return line[:i], b, nil
}

// merge sums the counts of identical blocks, which appear when several test
// binaries cover the same package.
func merge(blocks []Block) []Block {
	sort.Slice(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		if a.StartCol != b.StartCol {
			return a.StartCol < b.StartCol
		}
		if a.EndLine != b.EndLine {
			return a.EndLine < b.EndLine
		}
		return a.EndCol < b.EndCol
	})
	out := blocks[:0]
	for _, b := range blocks {
		if n := len(out); n > 0 && sameRange(out[n-1], b) {
			out[n-1].Count += b.Count
			continue
		}
		out = append(out, b)
	}
	return out
}

func sameRange(a, b Block) bool {
	return a.StartLine == b.StartLine && a.StartCol == b.StartCol &&
		a.EndLine == b.EndLine && a.EndCol == b.EndCol
}
