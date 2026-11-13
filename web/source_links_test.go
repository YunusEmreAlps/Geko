package web

import (
	"strings"
	"testing"
)

func TestSourceLinks(t *testing.T) {
	stack := strings.Join([]string{
		"\t/work/main.go:12 +0x2a",
		"at handler (C:\\My Project (local)\\main.ts:4:9)",
		"File \"/work/app.py\", line 22, in run",
		"at Program.Main() in D:\\work\\Program.cs:line 8",
		"at Example.run(Example.java:10)",
		"at vscode://extensions/install.go:2",
		"/work/../private.go:1",
		"\\\\remote\\share\\file.go:1",
		"/work/main.go:0",
		"/work/main.go:12",
	}, "\n")
	want := []string{
		"vscode://file/work/main.go:12",
		"vscode://file/C:/My%20Project%20%28local%29/main.ts:4:9",
		"vscode://file/work/app.py:22",
		"vscode://file/D:/work/Program.cs:8",
	}
	got := sourceLinks(stack)
	if len(got) != len(want) {
		t.Fatalf("source links = %+v", got)
	}
	for i, expected := range want {
		if string(got[i].URL) != expected {
			t.Errorf("link %d = %q, want %q", i, got[i].URL, expected)
		}
	}
}
