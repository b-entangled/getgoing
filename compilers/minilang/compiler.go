package minilang

import (
	"fmt"
	"strconv"
	"strings"
)

// Program is the output of compiling DSL source. Variables are assigned
// zero-based VM slots in declaration order.
type Program struct {
	Assembly  string
	Bytecode  []byte
	Variables []string
}

type compiler struct {
	assembly  strings.Builder
	variables map[string]int
	names     []string
	nextLabel int
}

// Compile lexes, parses, checks names, emits stack-machine assembly and
// assembles that text into bytecode.
func Compile(source string) (Program, error) {
	tokens, err := lex(source)
	if err != nil {
		return Program{}, err
	}
	statements, err := parse(tokens)
	if err != nil {
		return Program{}, err
	}
	c := compiler{variables: make(map[string]int)}
	for _, stmt := range statements {
		if err := c.compileStatement(stmt); err != nil {
			return Program{}, err
		}
	}
	c.emit("HALT")

	assembly := c.assembly.String()
	bytecode, err := Assemble(assembly)
	if err != nil {
		return Program{}, fmt.Errorf("compiler emitted invalid assembly: %w", err)
	}
	return Program{
		Assembly:  assembly,
		Bytecode:  bytecode,
		Variables: append([]string(nil), c.names...),
	}, nil
}

func (c *compiler) compileStatement(stmt statement) error {
	switch stmt := stmt.(type) {
	case letStmt:
		if _, exists := c.variables[stmt.name]; exists {
			return at(stmt.at, "variable %q is already declared", stmt.name)
		}
		if err := c.compileExpression(stmt.value); err != nil {
			return err
		}
		slot := len(c.names)
		c.variables[stmt.name] = slot
		c.names = append(c.names, stmt.name)
		c.emit("STORE %d", slot)
	case assignStmt:
		slot, exists := c.variables[stmt.name]
		if !exists {
			return at(stmt.at, "cannot assign to undeclared variable %q", stmt.name)
		}
		if err := c.compileExpression(stmt.value); err != nil {
			return err
		}
		c.emit("STORE %d", slot)
	case printStmt:
		if err := c.compileExpression(stmt.value); err != nil {
			return err
		}
		c.emit("PRINT")
	case whileStmt:
		start, end := c.label(), c.label()
		c.emit("%s:", start)
		if err := c.compileExpression(stmt.condition); err != nil {
			return err
		}
		c.emit("JZ %s", end)
		for _, bodyStmt := range stmt.body {
			if err := c.compileStatement(bodyStmt); err != nil {
				return err
			}
		}
		c.emit("JMP %s", start)
		c.emit("%s:", end)
	default:
		return fmt.Errorf("compiler: unsupported statement %T", stmt)
	}
	return nil
}

func (c *compiler) compileExpression(expr expression) error {
	switch expr := expr.(type) {
	case integerExpr:
		c.emit("PUSH %d", expr.value)
	case variableExpr:
		slot, exists := c.variables[expr.name]
		if !exists {
			return at(expr.at, "unknown variable %q", expr.name)
		}
		c.emit("LOAD %d", slot)
	case unaryExpr:
		if err := c.compileExpression(expr.value); err != nil {
			return err
		}
		if expr.operator != tokMinus {
			return at(expr.at, "unsupported unary operator")
		}
		c.emit("NEG")
	case binaryExpr:
		if err := c.compileExpression(expr.left); err != nil {
			return err
		}
		if err := c.compileExpression(expr.right); err != nil {
			return err
		}
		mnemonic, ok := binaryMnemonic(expr.operator)
		if !ok {
			return at(expr.at, "unsupported binary operator %q", tokenText(expr.operator))
		}
		c.emit("%s", mnemonic)
	default:
		return fmt.Errorf("compiler: unsupported expression %T", expr)
	}
	return nil
}

func (c *compiler) label() string {
	label := "L" + strconv.Itoa(c.nextLabel)
	c.nextLabel++
	return label
}

func (c *compiler) emit(format string, args ...any) {
	c.assembly.WriteString(fmt.Sprintf(format, args...))
	c.assembly.WriteByte('\n')
}

func binaryMnemonic(kind tokenKind) (string, bool) {
	switch kind {
	case tokPlus:
		return "ADD", true
	case tokMinus:
		return "SUB", true
	case tokStar:
		return "MUL", true
	case tokSlash:
		return "DIV", true
	case tokGreater:
		return "GT", true
	case tokEqual:
		return "EQ", true
	default:
		return "", false
	}
}

func tokenText(kind tokenKind) string {
	switch kind {
	case tokPlus:
		return "+"
	case tokMinus:
		return "-"
	case tokStar:
		return "*"
	case tokSlash:
		return "/"
	case tokGreater:
		return ">"
	case tokEqual:
		return "=="
	default:
		return "?"
	}
}

func at(t token, format string, args ...any) error {
	return fmt.Errorf("%d:%d: %s", t.line, t.col, fmt.Sprintf(format, args...))
}
