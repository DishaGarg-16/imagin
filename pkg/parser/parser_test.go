package parser

import (
	"strings"
	"testing"
)

func TestParseSimpleDockerfile(t *testing.T) {
	source := `
FROM ubuntu:22.04
ENV APP_HOME=/app
WORKDIR /app
COPY . /app
RUN apt-get update && apt-get install -y curl
EXPOSE 8080
CMD ["./start"]
`
	df, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(df.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(df.Stages))
	}

	stage := df.Stages[0]
	if stage.BaseName != "ubuntu" {
		t.Errorf("expected base name 'ubuntu', got %q", stage.BaseName)
	}
	if stage.BaseTag != "22.04" {
		t.Errorf("expected base tag '22.04', got %q", stage.BaseTag)
	}

	// FROM + ENV + WORKDIR + COPY + RUN + EXPOSE + CMD = 7 instructions
	if len(stage.Instructions) != 7 {
		t.Errorf("expected 7 instructions, got %d", len(stage.Instructions))
		for i, inst := range stage.Instructions {
			t.Logf("  [%d] %s: %v", i, inst.Type, inst.Args)
		}
	}
}

func TestParseMultiStage(t *testing.T) {
	source := `
FROM golang:1.22 AS builder
WORKDIR /src
COPY . .
RUN go build -o /app

FROM alpine:3.19
COPY --from=builder /app /app
CMD ["/app"]
`
	df, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(df.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(df.Stages))
	}

	// First stage
	s0 := df.Stages[0]
	if s0.Name != "builder" {
		t.Errorf("expected stage name 'builder', got %q", s0.Name)
	}
	if s0.BaseName != "golang" {
		t.Errorf("expected base name 'golang', got %q", s0.BaseName)
	}

	// Second stage
	s1 := df.Stages[1]
	if s1.BaseName != "alpine" {
		t.Errorf("expected base name 'alpine', got %q", s1.BaseName)
	}

	// Check COPY --from flag
	var copyInst *struct{ flags map[string]string }
	for _, inst := range s1.Instructions {
		if inst.Type == "COPY" {
			copyInst = &struct{ flags map[string]string }{inst.Flags}
			break
		}
	}
	if copyInst == nil {
		t.Fatal("COPY instruction not found in stage 2")
	}
	if copyInst.flags["from"] != "builder" {
		t.Errorf("expected COPY --from=builder, got --from=%q", copyInst.flags["from"])
	}
}

func TestParseContinuationLines(t *testing.T) {
	source := "FROM ubuntu:22.04\nRUN apt-get update && \\\n    apt-get install -y curl\n"
	df, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Should parse as FROM + RUN
	if len(df.Stages[0].Instructions) != 2 {
		t.Fatalf("expected 2 instructions, got %d", len(df.Stages[0].Instructions))
	}

	runInst := df.Stages[0].Instructions[1]
	if runInst.Type != "RUN" {
		t.Errorf("expected RUN, got %s", runInst.Type)
	}

	// The continuation should join the arguments
	raw := strings.Join(runInst.Args, " ")
	if !strings.Contains(raw, "apt-get update") || !strings.Contains(raw, "apt-get install") {
		t.Errorf("continuation line not properly joined: %q", raw)
	}
}

func TestParseGlobalArgs(t *testing.T) {
	source := `
ARG BASE_IMAGE=ubuntu:22.04
FROM ${BASE_IMAGE}
RUN echo hello
`
	df, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(df.GlobalArgs) != 1 {
		t.Fatalf("expected 1 global ARG, got %d", len(df.GlobalArgs))
	}

	if df.GlobalArgs[0].Args[0] != "BASE_IMAGE=ubuntu:22.04" {
		t.Errorf("unexpected global ARG: %v", df.GlobalArgs[0].Args)
	}
}

func TestParseComments(t *testing.T) {
	source := `
# This is a comment
FROM ubuntu:22.04
# Another comment
RUN echo hello
`
	df, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(df.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(df.Stages))
	}

	if len(df.Stages[0].Instructions) != 2 {
		t.Errorf("expected 2 instructions, got %d", len(df.Stages[0].Instructions))
	}
}

func TestParseErrorNoFrom(t *testing.T) {
	source := `RUN echo hello`
	_, err := Parse(source)
	if err == nil {
		t.Fatal("expected error for Dockerfile without FROM")
	}
}

func TestLexerTokenize(t *testing.T) {
	source := `FROM ubuntu:22.04
RUN echo hello`

	lexer := NewLexer(source)
	tokens := lexer.Tokenize()

	// Should have: FROM, ubuntu:22.04, \n, RUN, echo, hello, EOF
	instructionCount := 0
	argCount := 0
	for _, tok := range tokens {
		switch tok.Type {
		case TokenInstruction:
			instructionCount++
		case TokenArgument:
			argCount++
		}
	}

	if instructionCount != 2 {
		t.Errorf("expected 2 instructions, got %d", instructionCount)
	}
	if argCount != 3 {
		t.Errorf("expected 3 arguments (ubuntu:22.04, echo, hello), got %d", argCount)
	}
}

func TestParseJSONArray(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{`["./start"]`, []string{"./start"}},
		{`["cmd", "arg1", "arg2"]`, []string{"cmd", "arg1", "arg2"}},
		{`[]`, []string{}},
		{`invalid`, nil},
	}

	for _, tc := range tests {
		got := ParseJSONArray(tc.input)
		if tc.expected == nil {
			if got != nil {
				t.Errorf("ParseJSONArray(%q) = %v, want nil", tc.input, got)
			}
			continue
		}
		if len(got) != len(tc.expected) {
			t.Errorf("ParseJSONArray(%q) len = %d, want %d", tc.input, len(got), len(tc.expected))
			continue
		}
		for i := range got {
			if got[i] != tc.expected[i] {
				t.Errorf("ParseJSONArray(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.expected[i])
			}
		}
	}
}

func BenchmarkParse(b *testing.B) {
	source := `
FROM golang:1.22 AS builder
ARG VERSION=1.0.0
ENV GOPATH=/go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags "-X main.version=${VERSION}" -o /app
EXPOSE 8080
CMD ["./app"]

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /app /app
ENV APP_ENV=production
WORKDIR /
USER nobody
ENTRYPOINT ["/app"]
`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Parse(source)
		if err != nil {
			b.Fatal(err)
		}
	}
}
