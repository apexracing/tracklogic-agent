package builtin

import (
	"context"
	"fmt"
	"go-harness-tutorial/internal/model"
	"go-harness-tutorial/internal/tool"
)

type CalculatorTool struct {
	tool.BaseTool
}

func NewCalculator() *CalculatorTool {
	return &CalculatorTool{
		BaseTool: tool.NewBaseTool(
			"calculator",
			"Perform arithmetic calculations. Supports +, -, *, / operations.",
			[]model.ToolParameter{
				{Name: "expression", Type: "string", Description: "Arithmetic expression to evaluate (e.g. 25 * 4 + 15)", Required: true},
			},
		),
	}
}

func (c *CalculatorTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	expr, _ := args["expression"].(string)
	if expr == "" {
		return nil, fmt.Errorf("expression is required")
	}

	result, err := evaluate(expr)
	if err != nil {
		return nil, fmt.Errorf("calculation error: %w", err)
	}

	return map[string]any{
		"expression": expr,
		"result":     result,
	}, nil
}

func evaluate(expr string) (float64, error) {
	// Simple recursive descent parser for basic arithmetic
	// Supports: numbers, +, -, *, /, parentheses
	p := &parser{input: expr}
	return p.parseExpr()
}

type parser struct {
	input string
	pos   int
}

func (p *parser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *parser) advance() byte {
	c := p.input[p.pos]
	p.pos++
	return c
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) parseExpr() (float64, error) {
	result, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipWhitespace()
		c := p.peek()
		if c == '+' || c == '-' {
			p.advance()
			right, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			if c == '+' {
				result += right
			} else {
				result -= right
			}
		} else {
			break
		}
	}
	return result, nil
}

func (p *parser) parseTerm() (float64, error) {
	result, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipWhitespace()
		c := p.peek()
		if c == '*' || c == '/' {
			p.advance()
			right, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			if c == '*' {
				result *= right
			} else {
				if right == 0 {
					return 0, fmt.Errorf("division by zero")
				}
				result /= right
			}
		} else {
			break
		}
	}
	return result, nil
}

func (p *parser) parseFactor() (float64, error) {
	p.skipWhitespace()
	c := p.peek()
	if c == '(' {
		p.advance()
		result, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipWhitespace()
		if p.peek() != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.advance()
		return result, nil
	}
	return p.parseNumber()
}

func (p *parser) parseNumber() (float64, error) {
	p.skipWhitespace()
	start := p.pos
	for p.pos < len(p.input) && (isDigit(p.input[p.pos]) || p.input[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("expected number at position %d", start)
	}
	var val float64
	_, err := fmt.Sscanf(p.input[start:p.pos], "%f", &val)
	return val, err
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
