package minilang

import (
	"fmt"
	"strconv"
)

type expression interface{ expressionNode() }

type integerExpr struct {
	value int64
	at    token
}

func (integerExpr) expressionNode() {}

type variableExpr struct {
	name string
	at   token
}

func (variableExpr) expressionNode() {}

type unaryExpr struct {
	operator tokenKind
	value    expression
	at       token
}

func (unaryExpr) expressionNode() {}

type binaryExpr struct {
	left, right expression
	operator    tokenKind
	at          token
}

func (binaryExpr) expressionNode() {}

type statement interface{ statementNode() }

type letStmt struct {
	name  string
	value expression
	at    token
}

func (letStmt) statementNode() {}

type assignStmt struct {
	name  string
	value expression
	at    token
}

func (assignStmt) statementNode() {}

type printStmt struct {
	value expression
	at    token
}

func (printStmt) statementNode() {}

type whileStmt struct {
	condition expression
	body      []statement
	at        token
}

func (whileStmt) statementNode() {}

type parser struct {
	tokens  []token
	current int
}

func parse(tokens []token) ([]statement, error) {
	p := parser{tokens: tokens}
	statements := make([]statement, 0)
	for !p.check(tokEOF) {
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmt)
	}
	return statements, nil
}

func (p *parser) parseStatement() (statement, error) {
	switch p.peek().kind {
	case tokLet:
		at := p.advance()
		name, err := p.consume(tokIdentifier, "a variable name after 'let'")
		if err != nil {
			return nil, err
		}
		if _, err := p.consume(tokAssign, "'=' after the variable name"); err != nil {
			return nil, err
		}
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.consume(tokSemicolon, "';' after the declaration"); err != nil {
			return nil, err
		}
		return letStmt{name: name.text, value: value, at: at}, nil
	case tokPrint:
		at := p.advance()
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.consume(tokSemicolon, "';' after the print expression"); err != nil {
			return nil, err
		}
		return printStmt{value: value, at: at}, nil
	case tokWhile:
		at := p.advance()
		condition, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.consume(tokLeftBrace, "'{' before the while body"); err != nil {
			return nil, err
		}
		body := make([]statement, 0)
		for !p.check(tokRightBrace) && !p.check(tokEOF) {
			stmt, err := p.parseStatement()
			if err != nil {
				return nil, err
			}
			body = append(body, stmt)
		}
		if _, err := p.consume(tokRightBrace, "'}' after the while body"); err != nil {
			return nil, err
		}
		return whileStmt{condition: condition, body: body, at: at}, nil
	case tokIdentifier:
		if p.peekNext().kind != tokAssign {
			return nil, p.errorAt(p.peek(), "expected assignment; variable declarations use 'let'")
		}
		name := p.advance()
		p.advance() // '='
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.consume(tokSemicolon, "';' after the assignment"); err != nil {
			return nil, err
		}
		return assignStmt{name: name.text, value: value, at: name}, nil
	default:
		return nil, p.errorAt(p.peek(), "expected 'let', 'print', 'while', or an assignment")
	}
}

func (p *parser) parseExpression() (expression, error) { return p.parseComparison() }

func (p *parser) parseComparison() (expression, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if p.check(tokGreater) || p.check(tokEqual) {
		operator := p.advance()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{left: left, right: right, operator: operator.kind, at: operator}
	}
	return left, nil
}

func (p *parser) parseAdditive() (expression, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for p.check(tokPlus) || p.check(tokMinus) {
		operator := p.advance()
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{left: left, right: right, operator: operator.kind, at: operator}
	}
	return left, nil
}

func (p *parser) parseMultiplicative() (expression, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.check(tokStar) || p.check(tokSlash) {
		operator := p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{left: left, right: right, operator: operator.kind, at: operator}
	}
	return left, nil
}

func (p *parser) parseUnary() (expression, error) {
	if p.check(tokMinus) {
		operator := p.advance()
		value, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return unaryExpr{operator: operator.kind, value: value, at: operator}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (expression, error) {
	at := p.advance()
	switch at.kind {
	case tokInteger:
		value, err := strconv.ParseInt(at.text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%d:%d: integer literal %q is outside int64 range", at.line, at.col, at.text)
		}
		return integerExpr{value: value, at: at}, nil
	case tokIdentifier:
		return variableExpr{name: at.text, at: at}, nil
	case tokLeftParen:
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.consume(tokRightParen, "')' after the expression"); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, p.errorAt(at, "expected an integer, variable, unary '-', or parenthesized expression")
	}
}

func (p *parser) peek() token { return p.tokens[p.current] }

func (p *parser) peekNext() token {
	if p.current+1 >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.current+1]
}

func (p *parser) advance() token {
	t := p.peek()
	if t.kind != tokEOF {
		p.current++
	}
	return t
}

func (p *parser) check(kind tokenKind) bool { return p.peek().kind == kind }

func (p *parser) consume(kind tokenKind, expected string) (token, error) {
	if p.check(kind) {
		return p.advance(), nil
	}
	return token{}, p.errorAt(p.peek(), "expected "+expected)
}

func (p *parser) errorAt(t token, message string) error {
	return fmt.Errorf("%d:%d: %s, got %q", t.line, t.col, message, t.text)
}
