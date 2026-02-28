package parser

import (
	"fmt"
	"strings"

	"github.com/imagin/imagin/internal/types"
)

// Parser consumes tokens from the Lexer and produces a structured
// Dockerfile representation with build stages.
type Parser struct {
	tokens []Token
	pos    int

	// Pre-allocated instruction slice to reduce allocations.
	instructions []types.Instruction
}

// Parse parses a Dockerfile source string into a Dockerfile AST.
func Parse(source string) (*types.Dockerfile, error) {
	lexer := NewLexer(source)
	tokens := lexer.Tokenize()

	p := &Parser{
		tokens:       tokens,
		instructions: make([]types.Instruction, 0, 32),
	}

	return p.parse()
}

// parse is the main parsing loop.
func (p *Parser) parse() (*types.Dockerfile, error) {
	p.instructions = p.instructions[:0]

	// First pass: collect all instructions
	for !p.atEnd() {
		tok := p.current()

		switch tok.Type {
		case TokenComment, TokenNewline:
			p.advance()
			continue

		case TokenInstruction:
			inst, err := p.parseInstruction()
			if err != nil {
				return nil, err
			}
			p.instructions = append(p.instructions, inst)

		case TokenEOF:
			p.advance()

		default:
			return nil, fmt.Errorf("line %d: unexpected token %q (expected instruction keyword)", tok.Line, tok.Value)
		}
	}

	// Second pass: group instructions into stages
	return p.buildDockerfile()
}

// parseInstruction parses a single instruction (keyword + args + flags).
func (p *Parser) parseInstruction() (types.Instruction, error) {
	keyTok := p.current()
	instrType, ok := LookupInstruction(keyTok.Value)
	if !ok {
		return types.Instruction{}, fmt.Errorf("line %d: unknown instruction %q", keyTok.Line, keyTok.Value)
	}
	p.advance()

	inst := types.Instruction{
		Type:  instrType,
		Line:  keyTok.Line,
		Flags: make(map[string]string),
	}

	// Collect raw text for cache key computation
	var rawParts []string
	rawParts = append(rawParts, keyTok.Value)

	// Gather arguments and flags until newline or EOF
	for !p.atEnd() {
		tok := p.current()
		if tok.Type == TokenNewline || tok.Type == TokenEOF {
			p.advance()
			break
		}

		switch tok.Type {
		case TokenFlag:
			key, value := parseFlag(tok.Value)
			inst.Flags[key] = value
			rawParts = append(rawParts, tok.Value)

		case TokenArgument:
			inst.Args = append(inst.Args, tok.Value)
			rawParts = append(rawParts, tok.Value)

		case TokenComment:
			// Skip inline comments (rare but possible)
			p.advance()
			continue

		default:
			rawParts = append(rawParts, tok.Value)
		}

		p.advance()
	}

	inst.Raw = strings.Join(rawParts, " ")

	// Post-process specific instruction types.
	if err := p.postProcess(&inst); err != nil {
		return inst, err
	}

	return inst, nil
}

// postProcess applies instruction-specific validation and transformation.
func (p *Parser) postProcess(inst *types.Instruction) error {
	switch inst.Type {
	case types.InstructionFrom:
		if len(inst.Args) == 0 {
			return fmt.Errorf("line %d: FROM requires at least one argument", inst.Line)
		}
		// Parse "image:tag" if present
		// FROM image:tag AS name
		// Args might be: ["image:tag", "AS", "name"]
		// or ["image:tag"]

	case types.InstructionCmd, types.InstructionEntrypoint, types.InstructionShell:
		// If the first arg looks like a JSON array, parse it
		if len(inst.Args) == 1 && strings.HasPrefix(inst.Args[0], "[") {
			parsed := ParseJSONArray(inst.Args[0])
			if parsed != nil {
				inst.Args = parsed
			}
		}

	case types.InstructionEnv:
		// ENV supports two forms:
		//   ENV key=value key2=value2   (new form)
		//   ENV key value               (legacy form)
		// We normalise to key=value pairs.
		if len(inst.Args) == 2 && !strings.Contains(inst.Args[0], "=") {
			inst.Args = []string{inst.Args[0] + "=" + inst.Args[1]}
		}

	case types.InstructionExpose:
		// Validate port numbers
		for _, arg := range inst.Args {
			port := strings.TrimSuffix(strings.TrimSuffix(arg, "/tcp"), "/udp")
			for _, ch := range port {
				if ch < '0' || ch > '9' {
					return fmt.Errorf("line %d: EXPOSE invalid port %q", inst.Line, arg)
				}
			}
		}

	case types.InstructionCopy:
		if len(inst.Args) < 2 {
			return fmt.Errorf("line %d: COPY requires at least source and destination", inst.Line)
		}

	case types.InstructionRun:
		if len(inst.Args) == 0 {
			return fmt.Errorf("line %d: RUN requires a command", inst.Line)
		}
	}

	return nil
}

// buildDockerfile groups instructions into build stages.
func (p *Parser) buildDockerfile() (*types.Dockerfile, error) {
	df := &types.Dockerfile{
		Stages: make([]types.BuildStage, 0, 4),
	}

	stageIdx := -1

	for i := range p.instructions {
		inst := &p.instructions[i]

		if inst.Type == types.InstructionArg && stageIdx == -1 {
			// ARG before any FROM is a global build arg
			df.GlobalArgs = append(df.GlobalArgs, *inst)
			continue
		}

		if inst.Type == types.InstructionFrom {
			stageIdx++
			stage := types.BuildStage{
				Index: stageIdx,
			}

			// Parse FROM arguments: image[:tag] [AS name]
			if len(inst.Args) > 0 {
				ref := inst.Args[0]
				if colonIdx := strings.LastIndex(ref, ":"); colonIdx > 0 {
					stage.BaseName = ref[:colonIdx]
					stage.BaseTag = ref[colonIdx+1:]
				} else {
					stage.BaseName = ref
					stage.BaseTag = "latest"
				}
			}

			// Check for AS alias
			for j := 1; j < len(inst.Args)-1; j++ {
				if strings.EqualFold(inst.Args[j], "AS") && j+1 < len(inst.Args) {
					stage.Name = inst.Args[j+1]
					break
				}
			}

			stage.Instructions = []types.Instruction{*inst}
			inst.Stage = stageIdx
			df.Stages = append(df.Stages, stage)
			continue
		}

		if stageIdx == -1 {
			return nil, fmt.Errorf("line %d: instruction %s before FROM", inst.Line, inst.Type)
		}

		inst.Stage = stageIdx
		df.Stages[stageIdx].Instructions = append(df.Stages[stageIdx].Instructions, *inst)
	}

	if len(df.Stages) == 0 {
		return nil, fmt.Errorf("Dockerfile must contain at least one FROM instruction")
	}

	return df, nil
}

// parseFlag splits "--key=value" into (key, value). If no "=" is present
// the value is empty.
func parseFlag(flag string) (string, string) {
	// Strip leading "--"
	s := strings.TrimPrefix(flag, "--")
	if idx := strings.IndexByte(s, '='); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}

// --- Cursor helpers ---

func (p *Parser) current() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *Parser) advance() {
	if p.pos < len(p.tokens) {
		p.pos++
	}
}

func (p *Parser) atEnd() bool {
	return p.pos >= len(p.tokens)
}
