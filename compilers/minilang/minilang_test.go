package minilang

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestCompileAndRunFactorial(t *testing.T) {
	source := `
// Variables are assigned VM slots in declaration order.
let n = 5;
let result = 1;
while n > 1 {
    result = result * n;
    n = n - 1;
}
print result;
`
	program, err := Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(program.Assembly, "JZ L1") || !strings.Contains(program.Assembly, "JMP L0") {
		t.Fatalf("loop branches missing from assembly:\n%s", program.Assembly)
	}
	if len(program.Variables) != 2 || program.Variables[0] != "n" || program.Variables[1] != "result" {
		t.Fatalf("variables = %v, want [n result]", program.Variables)
	}
	output, err := Run(program.Bytecode, len(program.Variables))
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 || output[0] != 120 {
		t.Fatalf("output = %v, want [120]", output)
	}
}

func TestExpressionPrecedenceAndUnaryMinus(t *testing.T) {
	program, err := Compile("let value = -(2 + 3 * 4); print value;")
	if err != nil {
		t.Fatal(err)
	}
	output, err := Run(program.Bytecode, len(program.Variables))
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 || output[0] != -14 {
		t.Fatalf("output = %v, want [-14]", output)
	}
}

func TestEqualityAndIntegerDivision(t *testing.T) {
	program, err := Compile("print (7 / 2) + (3 == 3);")
	if err != nil {
		t.Fatal(err)
	}
	output, err := Run(program.Bytecode, len(program.Variables))
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 || output[0] != 4 {
		t.Fatalf("output = %v, want [4]", output)
	}
}

func TestCompileReportsSemanticAndSyntaxErrors(t *testing.T) {
	tests := []struct {
		name, source, want string
	}{
		{"unknown variable", "print missing;", "unknown variable"},
		{"duplicate declaration", "let x = 1; let x = 2;", "already declared"},
		{"assignment before declaration", "x = 1;", "undeclared variable"},
		{"missing expression", "let x = ;", "expected an integer"},
		{"missing terminator", "print 1", "expected ';'"},
		{"integer overflow", "print 9223372036854775808;", "outside int64 range"},
		{"unsupported character", "print 1.5;", "unexpected character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.source)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Compile(%q) error = %v, want substring %q", tt.source, err, tt.want)
			}
		})
	}
}

func TestAssemblerResolvesForwardAndBackwardLabels(t *testing.T) {
	assembly := `
	PUSH 3
	STORE 0
again:
	LOAD 0
	PUSH 1
	SUB
	STORE 0
	LOAD 0
	PUSH 0
	GT
	JZ finish
	JMP again
finish:
	PUSH 42
	PRINT
	HALT
`
	bytecode, err := Assemble(assembly)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Run(bytecode, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 || output[0] != 42 {
		t.Fatalf("output = %v, want [42]", output)
	}
}

func TestAssemblerRejectsInvalidAssembly(t *testing.T) {
	tests := []struct {
		name, source, want string
	}{
		{"unknown instruction", "MAGIC", "unknown instruction"},
		{"missing operand", "PUSH", "expects 1 operand"},
		{"extra operand", "HALT 1", "expects 0 operand"},
		{"unknown label", "JMP nowhere", "unknown label"},
		{"duplicate label", "loop:\nloop:\nHALT", "duplicate label"},
		{"negative slot", "LOAD -1", "slot cannot be negative"},
		{"malformed label", "1bad:\nHALT", "invalid label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Assemble(tt.source)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Assemble(%q) error = %v, want substring %q", tt.source, err, tt.want)
			}
		})
	}
}

func TestVMRejectsMalformedPrograms(t *testing.T) {
	tests := []struct {
		name, assembly, want string
	}{
		{"stack underflow", "ADD\nHALT", "stack underflow"},
		{"division by zero", "PUSH 5\nPUSH 0\nDIV\nHALT", "division by zero"},
		{"bad variable slot", "LOAD 2\nHALT", "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytecode, err := Assemble(tt.assembly)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Run(bytecode, 1)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run error = %v, want substring %q", err, tt.want)
			}
		})
	}
	if _, err := Run([]byte{byte(opHalt)}, 0); err == nil || !strings.Contains(err.Error(), "bytecode length") {
		t.Fatalf("Run malformed bytecode error = %v, want bytecode length error", err)
	}
	badJump := make([]byte, instructionWidth)
	badJump[0] = byte(opJump)
	binary.LittleEndian.PutUint64(badJump[1:], 9)
	if _, err := Run(badJump, 0); err == nil || !strings.Contains(err.Error(), "jump target") {
		t.Fatalf("Run invalid jump error = %v, want jump target error", err)
	}
}

func ExampleCompile() {
	program, err := Compile("let answer = 6 * 7; print answer;")
	if err != nil {
		panic(err)
	}
	output, err := Run(program.Bytecode, len(program.Variables))
	if err != nil {
		panic(err)
	}
	fmt.Println(output[0])
	// Output: 42
}
