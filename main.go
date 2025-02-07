package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

const (
	defaultConfigName = "xof.yaml"
	defaultModel      = "gemma2"
)

var langExtensions = map[string]string{
	"rs": "rust",
	"js": "javascript",
	"py": "python",
	"sh": "bash",
}

type Config struct {
	Model   string   `yaml:"model"`
	Output  string   `yaml:"output"`
	Prompt  string   `yaml:"prompt"`
	Script  string   `yaml:"script"`
	Attempt int      `yaml:"attempt"`
	Context []string `yaml:"context"`
}

func LoadConfig(r io.Reader) (c Config, err error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return
	}
	err = yaml.Unmarshal(b, &c)
	return
}

type ScriptResult struct {
	Stdout, Stderr string
	Error          error
}

func (s ScriptResult) String() string {
	msg := make([]string, 0, 6)
	if s.Error != nil {
		msg = append(msg, fmt.Sprintf("Error:\n%v", s.Error))
	}
	if s.Stdout != "" {
		msg = append(msg, fmt.Sprintf("Stdout:\n%s", s.Stdout))
	}
	if s.Stderr != "" {
		msg = append(msg, fmt.Sprintf("Stderr:\n%s", s.Stderr))
	}
	return strings.Join(msg, "\n\n")
}

func (s ScriptResult) Err() error {
	if s.Error != nil || s.Stderr != "" {
		return fmt.Errorf("%s", s.String())
	}
	return nil
}

func execute(ctx context.Context, script string) (result ScriptResult) {
	f, err := os.CreateTemp("", "")
	if err != nil {
		result.Error = err
		return
	}
	filename := f.Name()
	defer os.Remove(filename)
	if _, err := f.WriteString("#!/bin/bash\n\n" + script); err != nil {
		result.Error = err
		return
	}
	if err := f.Sync(); err != nil {
		result.Error = err
		return
	}
	if err := f.Close(); err != nil {
		result.Error = err
		return
	}
	cmd := exec.CommandContext(ctx, "bash", filename)
	stderr, stdout := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	result.Error = err
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() != 0 {
		if result.Error == nil {
			result.Error = fmt.Errorf("process exits with %d", cmd.ProcessState.ExitCode())
		}
	}
	return
}

func (c Config) execute(ctx context.Context) error {
	result := execute(ctx, c.Script)
	if err := result.Err(); err != nil {
		fmt.Println("=== FAILED with output ===")
		fmt.Println(result.Stderr)
		return err
	}
	fmt.Println("=== PASSED with output ===")
	fmt.Println(result.Stdout)
	return nil
}

func langFromFile(filename string) string {
	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	if lang, ok := langExtensions[ext]; ok {
		return lang
	}
	return ext
}

type CodeBlock struct {
	lang, content []byte
}

func NewCodeBlockFromFile(filename string) (c CodeBlock, err error) {
	lang := langFromFile(filename)
	c.lang = []byte(lang)
	c.content, err = os.ReadFile(filename)
	return
}

func (c CodeBlock) String() string {
	return "```" + string(c.lang) + "\n" + string(c.content) + "\n```"
}

func (c CodeBlock) WriteTo(filename string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	if _, err := f.Write(c.content); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func extractCodeBlocks(md string) []CodeBlock {
	src := []byte(md)
	r := text.NewReader(src)
	parser := goldmark.DefaultParser()
	root := parser.Parse(r)
	queue := []ast.Node{root}
	codeBlocks := []CodeBlock{}
	for len(queue) != 0 {
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if current == nil {
			continue
		}
		if current.Kind() == ast.KindFencedCodeBlock {
			block := current.(*ast.FencedCodeBlock)
			codeBlocks = append(codeBlocks, CodeBlock{
				lang:    block.Language(src),
				content: block.Lines().Value(src),
			})
		}
		queue = append(queue, current.NextSibling(), current.FirstChild())
	}
	return codeBlocks
}

func fileContext(files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	msg := "Given the following files:\n"
	for _, p := range files {
		b, err := NewCodeBlockFromFile(p)
		if err != nil {
			return msg, err
		}
		msg += fmt.Sprintf("\n# %s:\n%s\n", p, b)
	}
	return msg, nil
}

func (c Config) contexts() (string, error) {
	return fileContext(c.Context)
}

func (c Config) prompt() (string, error) {
	system := "You are a senior software engineer who write clean and precise code with detailed comments. Returns code ONLY."
	contexts, err := c.contexts()
	if err != nil {
		return "", err
	}
	messages := []string{system}
	if contexts != "" {
		messages = append(messages, contexts)
	}
	if c.Prompt != "" {
		messages = append(messages, c.Prompt)
	}
	return strings.Join(messages, "\n\n"), nil
}

func (c Config) promptWithError(code CodeBlock, err error) (string, error) {
	addon := fmt.Sprintf("However, with the following generated code for the above: \n%s\n\nIt failed with:\n%v\nFix the generated code and return fixed code ONLY.", code, err)
	prompt, err := c.prompt()
	if err != nil {
		return "", err
	}
	return prompt + "\n\n" + addon, nil
}

func (c Config) model() string {
	if c.Model != "" {
		return c.Model
	}
	return defaultModel
}

func generate(ctx context.Context, model, prompt string) (response string, err error) {
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return
	}
	req := &api.GenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: new(bool),
	}
	respFunc := func(r api.GenerateResponse) error {
		response = r.Response
		return nil
	}
	err = client.Generate(ctx, req, respFunc)
	return
}

func (c Config) code(ctx context.Context, prompt string) (CodeBlock, error) {
	res, err := generate(ctx, c.model(), prompt)
	if err != nil {
		return CodeBlock{}, err
	}
	generatedCode := extractCodeBlocks(res)
	var code CodeBlock
	lang := langFromFile(c.Output)
	for _, block := range generatedCode {
		if string(block.lang) == lang {
			code = block
		}
	}
	if len(code.content) == 0 {
		return CodeBlock{}, fmt.Errorf("no code is generated.")
	}
	fmt.Println("=== generated code ===")
	fmt.Println(code)
	if err := code.WriteTo(c.Output); err != nil {
		return code, err
	}
	if err := c.execute(ctx); err != nil {
		return code, err
	}
	return code, nil
}

func (c Config) Generate(ctx context.Context) error {
	prompt, err := c.prompt()
	if err != nil {
		return err
	}
	for attempt := 0; attempt < c.Attempt; attempt++ {
		code, err := c.code(ctx, prompt)
		if err == nil {
			return nil
		}
		var promptErr error
		prompt, promptErr = c.promptWithError(code, err)
		if promptErr != nil {
			return promptErr
		}
	}
	return fmt.Errorf("attempted %d time(s) and failed", c.Attempt)
}

func main() {
	// TODO: implement recursive lookup of the config file in current and parent folders
	f, err := os.Open(defaultConfigName)
	if err != nil {
		log.Fatal(err)
	}
	config, err := LoadConfig(f)
	if err != nil {
		log.Fatal(err)
	}
	if err := config.Generate(context.Background()); err != nil {
		log.Fatal(err)
	}
}
