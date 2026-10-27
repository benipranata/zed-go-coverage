package coverprofile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := `mode: set
example.com/m/a.go:10.2,12.3 2 1
example.com/m/a.go:3.1,4.5 1 0
example.com/m/a.go:3.1,4.5 1 2

example.com/m/b.go:1.1,1.9 1 0
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{
		"example.com/m/a.go": {
			{3, 1, 4, 5, 1, 2},
			{10, 2, 12, 3, 2, 1},
		},
		"example.com/m/b.go": {{1, 1, 1, 9, 1, 0}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseSortsByEveryPositionField(t *testing.T) {
	in := `mode: set
f.go:1.2,3.4 1 0
f.go:1.1,3.5 1 0
f.go:1.1,3.4 1 0
f.go:1.1,2.9 1 0
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Block{{1, 1, 2, 9, 1, 0}, {1, 1, 3, 4, 1, 0}, {1, 1, 3, 5, 1, 0}, {1, 2, 3, 4, 1, 0}}
	if !reflect.DeepEqual(got["f.go"], want) {
		t.Fatalf("got %v, want %v", got["f.go"], want)
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no mode":       "a.go:1.1,2.2 1 0\n",
		"no colon":      "mode: set\nnonsense\n",
		"bad numbers":   "mode: set\na.go:1.1,2.2 x 0\n",
		"line too long": "mode: set\n" + strings.Repeat("x", 1<<20+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(in)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestParseReadError(t *testing.T) {
	if _, err := Parse(failingReader{}); err == nil {
		t.Fatal("expected error")
	}
}
