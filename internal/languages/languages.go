// Package languages holds the images and commands used to build and run each supported language.
package languages

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// stackPlaceholder in a run command is replaced with a stack size that fits the memory limit
const stackPlaceholder = "{stack_mb}"

type Language struct {
	ID         string
	Name       string
	Aliases    []string
	Extensions []string
	Image      string
	SourceFile string

	// Compile runs once per submission. Interpreted languages use it as a syntax check
	// so broken code is reported as a compilation error instead of failing every test.
	Compile    []string
	CompileEnv []string
	Run        []string
	RunEnv     []string

	// text a runtime prints to stderr when it runs out of memory on its own,
	// which happens before the container itself gets OOM killed
	OOMMarkers []string
}

var registry = []Language{
	{
		ID:         "c",
		Name:       "C (GCC 15, gnu17)",
		Extensions: []string{".c"},
		Image:      "gcc:15",
		SourceFile: "main.c",
		Compile:    []string{"gcc", "-O2", "-std=gnu17", "-pipe", "-o", "main", "main.c", "-lm"},
		Run:        []string{"./main"},
	},
	{
		ID:         "cpp",
		Name:       "C++ (GCC 15, gnu++20)",
		Aliases:    []string{"c++", "cxx"},
		Extensions: []string{".cpp", ".cc", ".cxx", ".c++"},
		Image:      "gcc:15",
		SourceFile: "main.cpp",
		Compile:    []string{"g++", "-O2", "-std=gnu++20", "-pipe", "-o", "main", "main.cpp"},
		Run:        []string{"./main"},
		OOMMarkers: []string{"std::bad_alloc"},
	},
	{
		ID:         "go",
		Name:       "Go 1.27",
		Aliases:    []string{"golang"},
		Extensions: []string{".go"},
		Image:      "golang:1.27-alpine",
		SourceFile: "main.go",
		Compile:    []string{"go", "build", "-trimpath", "-o", "main", "main.go"},
		// no network in the sandbox, so make sure go never tries to download anything
		CompileEnv: []string{"GOCACHE=/tmp/gocache", "GOPATH=/tmp/gopath", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off"},
		Run:        []string{"./main"},
		OOMMarkers: []string{"runtime: out of memory"},
	},
	{
		ID:         "java",
		Name:       "Java 25 (Temurin)",
		Extensions: []string{".java"},
		Image:      "eclipse-temurin:25-jdk-alpine",
		SourceFile: "Main.java",
		Compile:    []string{"javac", "-J-XX:-UsePerfData", "-encoding", "UTF-8", "-d", ".", "Main.java"},
		// the JVM only takes a quarter of the container memory for heap unless told otherwise
		Run:        []string{"java", "-XX:-UsePerfData", "-XX:+UseSerialGC", "-XX:MaxRAMPercentage=75", "-Xss" + stackPlaceholder + "m", "-cp", ".", "Main"},
		OOMMarkers: []string{"java.lang.OutOfMemoryError"},
	},
	{
		ID:         "javascript",
		Name:       "JavaScript (Node.js 24)",
		Aliases:    []string{"js", "node", "nodejs"},
		Extensions: []string{".js", ".mjs", ".cjs"},
		Image:      "node:24-alpine",
		SourceFile: "main.js",
		Compile:    []string{"node", "--check", "main.js"},
		Run:        []string{"node", "main.js"},
		OOMMarkers: []string{"JavaScript heap out of memory"},
	},
	{
		ID:         "python",
		Name:       "Python 3.14",
		Aliases:    []string{"py", "python3", "py3"},
		Extensions: []string{".py"},
		Image:      "python:3.14-alpine",
		SourceFile: "main.py",
		Compile:    []string{"python", "-m", "py_compile", "main.py"},
		CompileEnv: []string{"PYTHONPYCACHEPREFIX=/tmp/pycache"},
		Run:        []string{"python", "main.py"},
		RunEnv:     []string{"PYTHONDONTWRITEBYTECODE=1", "PYTHONIOENCODING=utf-8"},
		OOMMarkers: []string{"MemoryError"},
	},
	{
		ID:         "rust",
		Name:       "Rust 1.99 (edition 2021)",
		Aliases:    []string{"rs"},
		Extensions: []string{".rs"},
		Image:      "rust:1.99-alpine",
		SourceFile: "main.rs",
		Compile:    []string{"rustc", "-O", "--edition", "2021", "-o", "main", "main.rs"},
		CompileEnv: []string{"TMPDIR=/tmp"},
		Run:        []string{"./main"},
		OOMMarkers: []string{"memory allocation of"},
	},
}

// All returns every supported language. The slice is a deep copy so callers can't change the registry.
func All() []Language {
	out := make([]Language, len(registry))
	for i, l := range registry {
		out[i] = l.clone()
	}
	return out
}

func (l Language) clone() Language {
	l.Aliases = slices.Clone(l.Aliases)
	l.Extensions = slices.Clone(l.Extensions)
	l.Compile = slices.Clone(l.Compile)
	l.CompileEnv = slices.Clone(l.CompileEnv)
	l.Run = slices.Clone(l.Run)
	l.RunEnv = slices.Clone(l.RunEnv)
	l.OOMMarkers = slices.Clone(l.OOMMarkers)
	return l
}

// RunCommand returns the run command for a given memory limit.
func (l Language) RunCommand(memoryMB int) []string {
	// a deep recursion needs far more memory than the stack size itself, at 1/8 of the
	// limit a StackOverflowError still shows up as a runtime error instead of an OOM kill
	stack := strconv.Itoa(min(max(memoryMB/8, 4), 64))
	out := make([]string, len(l.Run))
	for i, arg := range l.Run {
		out[i] = strings.ReplaceAll(arg, stackPlaceholder, stack)
	}
	return out
}

// Lookup finds a language by id or alias, ignoring case and surrounding spaces.
func Lookup(name string) (Language, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return Language{}, false
	}
	for _, l := range registry {
		if l.ID == name || slices.Contains(l.Aliases, name) {
			return l.clone(), true
		}
	}
	return Language{}, false
}

// ByExtension maps a file extension like ".py" to its language.
func ByExtension(ext string) (Language, bool) {
	ext = strings.ToLower(ext)
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	for _, l := range registry {
		if slices.Contains(l.Extensions, ext) {
			return l.clone(), true
		}
	}
	return Language{}, false
}

// Select resolves the configured language list. An empty list means all of them.
func Select(names []string) ([]Language, error) {
	if len(names) == 0 {
		return All(), nil
	}
	var out []Language
	for _, name := range names {
		l, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown language %q", name)
		}
		if !slices.ContainsFunc(out, func(x Language) bool { return x.ID == l.ID }) {
			out = append(out, l)
		}
	}
	return out, nil
}
