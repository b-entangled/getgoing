package minilang

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

type assemblyInstruction struct {
	definition instruction
	operand    string
	line       int
}

// Assemble translates the package's readable stack assembly into fixed-width
// bytecode. Labels are resolved in a first pass; operands are encoded in a
// second pass.
func Assemble(source string) ([]byte, error) {
	labels := make(map[string]int64)
	instructions := make([]assemblyInstruction, 0)
	for lineIndex, raw := range strings.Split(source, "\n") {
		lineNo := lineIndex + 1
		line, _, _ := strings.Cut(raw, "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if strings.HasSuffix(fields[0], ":") {
			name := strings.TrimSuffix(fields[0], ":")
			if !validSymbol(name) {
				return nil, fmt.Errorf("line %d: invalid label %q", lineNo, name)
			}
			if _, exists := labels[name]; exists {
				return nil, fmt.Errorf("line %d: duplicate label %q", lineNo, name)
			}
			labels[name] = int64(len(instructions))
			fields = fields[1:]
			if len(fields) == 0 {
				continue
			}
		} else if strings.Contains(fields[0], ":") {
			return nil, fmt.Errorf("line %d: label must end with ':'", lineNo)
		}

		mnemonic := strings.ToUpper(fields[0])
		definition, exists := instructionSet[mnemonic]
		if !exists {
			return nil, fmt.Errorf("line %d: unknown instruction %q", lineNo, fields[0])
		}
		want := 1
		if definition.operand != noOperand {
			want++
		}
		if len(fields) != want {
			return nil, fmt.Errorf("line %d: %s expects %d operand(s), got %d", lineNo, mnemonic, want-1, len(fields)-1)
		}
		operand := ""
		if want == 2 {
			operand = fields[1]
		}
		instructions = append(instructions, assemblyInstruction{
			definition: definition,
			operand:    operand,
			line:       lineNo,
		})
	}

	code := make([]byte, 0, len(instructions)*instructionWidth)
	for _, instruction := range instructions {
		operand, err := encodeOperand(instruction, labels)
		if err != nil {
			return nil, err
		}
		code = append(code, byte(instruction.definition.opcode))
		var encoded [8]byte
		binary.LittleEndian.PutUint64(encoded[:], uint64(operand))
		code = append(code, encoded[:]...)
	}
	return code, nil
}

func encodeOperand(instruction assemblyInstruction, labels map[string]int64) (int64, error) {
	if instruction.definition.operand == noOperand {
		return 0, nil
	}
	if instruction.definition.operand == labelOperand {
		address, exists := labels[instruction.operand]
		if !exists {
			return 0, fmt.Errorf("line %d: unknown label %q", instruction.line, instruction.operand)
		}
		return address, nil
	}

	value, err := strconv.ParseInt(instruction.operand, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("line %d: invalid integer operand %q", instruction.line, instruction.operand)
	}
	if instruction.definition.operand == slotOperand && value < 0 {
		return 0, fmt.Errorf("line %d: variable slot cannot be negative", instruction.line)
	}
	return value, nil
}

func validSymbol(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !isIdentifierStart(r) {
				return false
			}
		} else if !isIdentifierContinue(r) {
			return false
		}
	}
	return true
}
