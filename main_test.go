package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_LoadConfig(t *testing.T) {
	cases := map[string]struct {
		path string
		err  error
	}{
		"base": {
			path: "testdata/test.yaml",
		},
	}
	for name := range cases {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(tc.path)
			require.NoError(t, err)
			_, err = LoadConfig(f)
			require.ErrorIs(t, tc.err, err)
		})
	}
}

func Test_execute(t *testing.T) {
	cases := map[string]struct {
		script string
		want   ScriptResult
		err    error
	}{
		"base": {
			script: "echo \"hi\"",
			want:   ScriptResult{Stdout: "hi\n"},
		},
		"stderr": {
			script: "echo stdout; echo 1>&2 stderr",
			want: ScriptResult{
				Stdout: "stdout\n",
				Stderr: "stderr\n",
			},
			err: fmt.Errorf("Stdout:\nstdout\n\n\nStderr:\nstderr\n"),
		},
	}
	for name := range cases {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			got := execute(context.TODO(), tc.script)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.err, got.Err())
		})
	}
}

func Test_extractCodeBlocks(t *testing.T) {
	cases := map[string]struct {
		path string
		want []CodeBlock
	}{
		"base": {
			path: "testdata/prompt_response.md",
			want: []CodeBlock{
				{
					lang: []byte(`go`),
					content: []byte(`# my_struct_is_awesome.go
package util

type MyStruct struct {
  A string
  B int
}
`),
				},
			},
		},
	}
	for name := range cases {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(tc.path)
			require.NoError(t, err)
			require.Equal(t, tc.want, extractCodeBlocks(string(b)))
		})
	}
}

func Test_Generate(t *testing.T) {
	dir := os.TempDir()
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "main.go")
	c := Config{
		Output:  output,
		Prompt:  "Write a golang program that prints 'hello' to stdout.",
		Script:  "go run " + output,
		Attempt: 10,
	}
	require.NoError(t, c.Generate(context.TODO()))
}
