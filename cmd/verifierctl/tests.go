package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

type testCase struct {
	Name           string `json:"name"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
}

// loadTests reads either a JSON file or a folder with matching NAME.in and NAME.out files.
func loadTests(path string) ([]*pb.TestCase, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var cases []testCase
	if info.IsDir() {
		cases, err = loadTestDir(path)
	} else {
		cases, err = loadTestFile(path)
	}
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("%s has no test cases", path)
	}
	out := make([]*pb.TestCase, len(cases))
	for i, c := range cases {
		out[i] = &pb.TestCase{Name: c.Name, Input: c.Input, ExpectedOutput: c.ExpectedOutput}
	}
	return out, nil
}

// accepts a plain array or an object with a test_cases array
func loadTestFile(path string) ([]testCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		var wrapped struct {
			TestCases []testCase `json:"test_cases"`
		}
		if err := strictUnmarshal(data, &wrapped); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		return wrapped.TestCases, nil
	}
	var cases []testCase
	if err := strictUnmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return cases, nil
}

func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func loadTestDir(dir string) ([]testCase, error) {
	inputs, err := filepath.Glob(filepath.Join(dir, "*.in"))
	if err != nil {
		return nil, err
	}
	sort.Slice(inputs, func(i, j int) bool { return naturalLess(filepath.Base(inputs[i]), filepath.Base(inputs[j])) })

	var cases []testCase
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".in")
		input, err := os.ReadFile(in)
		if err != nil {
			return nil, err
		}
		expected, err := readFirst(filepath.Join(dir, name+".out"), filepath.Join(dir, name+".ans"))
		if err != nil {
			return nil, fmt.Errorf("test %s has an input but no %s.out or %s.ans file", name, name, name)
		}
		cases = append(cases, testCase{Name: name, Input: string(input), ExpectedOutput: string(expected)})
	}
	return cases, nil
}

func readFirst(paths ...string) ([]byte, error) {
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, os.ErrNotExist
}

// naturalLess sorts "2.in" before "10.in"
func naturalLess(a, b string) bool {
	na, errA := strconv.Atoi(strings.SplitN(a, ".", 2)[0])
	nb, errB := strconv.Atoi(strings.SplitN(b, ".", 2)[0])
	if errA == nil && errB == nil && na != nb {
		return na < nb
	}
	return a < b
}
