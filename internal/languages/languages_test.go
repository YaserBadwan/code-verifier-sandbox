package languages

import (
	"slices"
	"strings"
	"testing"
)

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]string{}
	for _, l := range All() {
		if l.ID == "" || l.Name == "" || l.Image == "" || l.SourceFile == "" || len(l.Run) == 0 || len(l.Compile) == 0 {
			t.Fatalf("language %+v is missing required fields", l)
		}
		for _, key := range append([]string{l.ID}, l.Aliases...) {
			if key != strings.ToLower(key) {
				t.Fatalf("%q should be lower case", key)
			}
			if other, dup := seen[key]; dup {
				t.Fatalf("%q is used by both %s and %s", key, other, l.ID)
			}
			seen[key] = l.ID
		}
	}
	if len(All()) != 7 {
		t.Fatalf("expected 7 languages, got %d", len(All()))
	}
}

func TestLookup(t *testing.T) {
	cases := map[string]string{
		"python": "python", "PY": "python", " py3 ": "python",
		"c++": "cpp", "cpp": "cpp", "js": "javascript", "node": "javascript",
		"golang": "go", "rs": "rust", "java": "java", "c": "c",
	}
	for in, want := range cases {
		l, ok := Lookup(in)
		if !ok || l.ID != want {
			t.Fatalf("Lookup(%q) = %q %v, want %q", in, l.ID, ok, want)
		}
	}
	if _, ok := Lookup("brainfuck"); ok {
		t.Fatal("unknown language should not resolve")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("empty language should not resolve")
	}
}

func TestJavaUsesMainClass(t *testing.T) {
	l, _ := Lookup("java")
	if l.SourceFile != "Main.java" {
		t.Fatalf("java must be saved as Main.java, got %s", l.SourceFile)
	}
}

func TestSelect(t *testing.T) {
	all, err := Select(nil)
	if err != nil || len(all) != len(All()) {
		t.Fatalf("empty selection should return everything, got %d %v", len(all), err)
	}

	some, err := Select([]string{"py", "python", "cpp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(some) != 2 || some[0].ID != "python" || some[1].ID != "cpp" {
		t.Fatalf("expected python and cpp once each, got %+v", some)
	}

	if _, err := Select([]string{"python", "cobol"}); err == nil || !strings.Contains(err.Error(), "cobol") {
		t.Fatalf("expected an error naming cobol, got %v", err)
	}
}

func TestByExtension(t *testing.T) {
	cases := map[string]string{".py": "python", ".CPP": "cpp", ".cc": "cpp", ".rs": "rust", ".mjs": "javascript", "go": "go"}
	for ext, want := range cases {
		l, ok := ByExtension(ext)
		if !ok || l.ID != want {
			t.Fatalf("ByExtension(%q) = %q %v, want %q", ext, l.ID, ok, want)
		}
	}
	if _, ok := ByExtension(".txt"); ok {
		t.Fatal(".txt should not map to a language")
	}
}

func TestAllReturnsACopy(t *testing.T) {
	a := All()
	a[0].Image = "changed"
	if All()[0].Image == "changed" {
		t.Fatal("callers must not be able to modify the registry")
	}
}

func TestRunCommandScalesJavaStack(t *testing.T) {
	java, _ := Lookup("java")
	cases := map[int]string{32: "-Xss4m", 256: "-Xss32m", 1024: "-Xss64m"}
	for mem, want := range cases {
		if !slices.Contains(java.RunCommand(mem), want) {
			t.Fatalf("memory %d should use %s, got %v", mem, want, java.RunCommand(mem))
		}
	}
	py, _ := Lookup("python")
	if !slices.Equal(py.RunCommand(256), py.Run) {
		t.Fatal("languages without the placeholder should be unchanged")
	}
	if slices.Contains(java.Run, "-Xss32m") {
		t.Fatal("RunCommand must not modify the registry")
	}
}
