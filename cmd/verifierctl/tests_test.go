package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTestsArray(t *testing.T) {
	p := write(t, t.TempDir(), "t.json", `[{"name":"a","input":"1 2\n","expected_output":"3\n"},{"input":"","expected_output":""}]`)
	tests, err := loadTests(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 || tests[0].Name != "a" || tests[0].Input != "1 2\n" || tests[0].ExpectedOutput != "3\n" {
		t.Fatalf("got %v", tests)
	}
}

func TestLoadTestsWrapped(t *testing.T) {
	p := write(t, t.TempDir(), "t.json", ` {"test_cases":[{"input":"x","expected_output":"y"}]}`)
	tests, err := loadTests(p)
	if err != nil || len(tests) != 1 {
		t.Fatalf("got %v %v", tests, err)
	}
}

func TestLoadTestsErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"typo.json":  `[{"inptu":"x"}]`,
		"empty.json": `[]`,
		"bad.json":   `[{`,
	}
	for name, content := range cases {
		if _, err := loadTests(write(t, dir, name, content)); err == nil {
			t.Fatalf("%s should fail", name)
		}
	}
	if _, err := loadTests(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file should fail")
	}
}

func TestLoadTestDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "10.in", "ten")
	write(t, dir, "10.out", "TEN")
	write(t, dir, "2.in", "two")
	write(t, dir, "2.ans", "TWO")
	write(t, dir, "notes.txt", "ignored")
	tests, err := loadTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 || tests[0].Name != "2" || tests[0].ExpectedOutput != "TWO" || tests[1].Name != "10" {
		t.Fatalf("expected natural order with .ans support, got %v", tests)
	}

	write(t, dir, "3.in", "three")
	if _, err := loadTests(dir); err == nil || !strings.Contains(err.Error(), "3.out") {
		t.Fatalf("missing answer file should be reported, got %v", err)
	}
}

func TestParseCompareMode(t *testing.T) {
	for _, ok := range []string{"exact", "TOKENS", "trailing", ""} {
		if _, err := parseCompareMode(ok); err != nil {
			t.Fatalf("%q should parse, got %v", ok, err)
		}
	}
	if _, err := parseCompareMode("fuzzy"); err == nil {
		t.Fatal("fuzzy should be rejected")
	}
}

func TestUsageAndUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != exitError || !strings.Contains(out.String(), "Usage") {
		t.Fatalf("no args should print usage and fail, got %d", code)
	}
	out.Reset()
	if code := run([]string{"help"}, &out, &errOut); code != exitAccepted {
		t.Fatalf("help should succeed, got %d", code)
	}
	errOut.Reset()
	if code := run([]string{"dance"}, &out, &errOut); code != exitError || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("unknown command should fail, got %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"run", "-file", "x.py"}, &out, &errOut); code != exitError || !strings.Contains(errOut.String(), "-tests") {
		t.Fatalf("missing -tests should fail, got %d %q", code, errOut.String())
	}
}
