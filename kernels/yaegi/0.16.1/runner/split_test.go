package main

import (
	"reflect"
	"testing"
)

func TestSplitChunks(t *testing.T) {
	tests := []struct {
		name string
		code string
		want []string
	}{
		{
			name: "pure statements stay one chunk",
			code: "a := 1\nb := 2\nfmt.Println(a + b)",
			want: []string{"a := 1\nb := 2\nfmt.Println(a + b)"},
		},
		{
			name: "func declaration then call splits",
			code: "func demo() {\n\tfmt.Println(\"hi\")\n}\ndemo()",
			want: []string{"func demo() {\n\tfmt.Println(\"hi\")\n}", "demo()"},
		},
		{
			name: "statements then func then statements",
			code: "x := 1\nfunc f() int { return x }\nfmt.Println(f())",
			want: []string{"x := 1", "func f() int { return x }", "fmt.Println(f())"},
		},
		{
			name: "var declaration then use splits",
			code: "var z = 10\nfmt.Println(z)",
			want: []string{"var z = 10", "fmt.Println(z)"},
		},
		{
			name: "grouped import stays whole",
			code: "import (\n\t\"fmt\"\n\t\"sort\"\n)",
			want: []string{"import (\n\t\"fmt\"\n\t\"sort\"\n)"},
		},
		{
			name: "import block then statement splits",
			code: "import \"fmt\"\nfmt.Println(1)",
			want: []string{"import \"fmt\"", "fmt.Println(1)"},
		},
		{
			name: "type declaration then use splits",
			code: "type Point struct{ X, Y int }\np := Point{1, 2}\nfmt.Println(p.X)",
			want: []string{"type Point struct{ X, Y int }", "p := Point{1, 2}\nfmt.Println(p.X)"},
		},
		{
			name: "brace inside a string does not break depth",
			code: "s := \"}{\"\nfunc g() {}\ng()",
			want: []string{"s := \"}{\"", "func g() {}", "g()"},
		},
		{
			name: "blank lines stay with the surrounding chunk",
			code: "a := 1\n\nfmt.Println(a)",
			want: []string{"a := 1\n\nfmt.Println(a)"},
		},
		{
			name: "two consecutive declarations are separate chunks",
			code: "func a() {}\nfunc b() {}",
			want: []string{"func a() {}", "func b() {}"},
		},
		{
			name: "unparseable block is returned whole",
			code: "this is not ) go (",
			want: []string{"this is not ) go ("},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitChunks(tc.code)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitChunks()\n got = %#v\nwant = %#v", got, tc.want)
			}
		})
	}
}
