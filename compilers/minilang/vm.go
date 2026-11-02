package minilang

import (
	"encoding/binary"
	"fmt"
)

const maxExecutionSteps = 10_000_000

// Run executes bytecode produced by Assemble or Compile and returns values
// written by PRINT, in order. Execution is capped to stop accidental infinite
// loops from running forever.
func Run(bytecode []byte, variableCount int) ([]int64, error) {
	if variableCount < 0 {
		return nil, fmt.Errorf("variable count cannot be negative")
	}
	if len(bytecode) == 0 || len(bytecode)%instructionWidth != 0 {
		return nil, fmt.Errorf("bytecode length %d is not a non-empty sequence of %d-byte instructions", len(bytecode), instructionWidth)
	}

	instructionCount := int64(len(bytecode) / instructionWidth)
	variables := make([]int64, variableCount)
	stack := make([]int64, 0, 16)
	output := make([]int64, 0)
	pc := int64(0)
	pop := func() (int64, error) {
		if len(stack) == 0 {
			return 0, fmt.Errorf("instruction %d: stack underflow", pc-1)
		}
		last := len(stack) - 1
		value := stack[last]
		stack = stack[:last]
		return value, nil
	}
	push := func(value int64) { stack = append(stack, value) }

	for step := 0; step < maxExecutionSteps; step++ {
		if pc < 0 || pc >= instructionCount {
			return nil, fmt.Errorf("program counter %d is outside bytecode; program has no reachable HALT", pc)
		}
		offset := int(pc) * instructionWidth
		instructionOpcode := opcode(bytecode[offset])
		operand := int64(binary.LittleEndian.Uint64(bytecode[offset+1 : offset+instructionWidth]))
		pc++

		switch instructionOpcode {
		case opHalt:
			return output, nil
		case opPush:
			push(operand)
		case opLoad:
			if operand < 0 || operand >= int64(len(variables)) {
				return nil, fmt.Errorf("instruction %d: variable slot %d is out of range", pc-1, operand)
			}
			push(variables[operand])
		case opStore:
			if operand < 0 || operand >= int64(len(variables)) {
				return nil, fmt.Errorf("instruction %d: variable slot %d is out of range", pc-1, operand)
			}
			value, err := pop()
			if err != nil {
				return nil, err
			}
			variables[operand] = value
		case opAdd, opSub, opMul, opDiv, opGreater, opEqual:
			right, err := pop()
			if err != nil {
				return nil, err
			}
			left, err := pop()
			if err != nil {
				return nil, err
			}
			var result int64
			switch instructionOpcode {
			case opAdd:
				result = left + right
			case opSub:
				result = left - right
			case opMul:
				result = left * right
			case opDiv:
				if right == 0 {
					return nil, fmt.Errorf("instruction %d: division by zero", pc-1)
				}
				result = left / right
			case opGreater:
				if left > right {
					result = 1
				}
			case opEqual:
				if left == right {
					result = 1
				}
			}
			push(result)
		case opNeg:
			value, err := pop()
			if err != nil {
				return nil, err
			}
			push(-value)
		case opJump:
			if operand < 0 || operand >= instructionCount {
				return nil, fmt.Errorf("instruction %d: jump target %d is out of range", pc-1, operand)
			}
			pc = operand
		case opJumpIfZero:
			condition, err := pop()
			if err != nil {
				return nil, err
			}
			if condition == 0 {
				if operand < 0 || operand >= instructionCount {
					return nil, fmt.Errorf("instruction %d: jump target %d is out of range", pc-1, operand)
				}
				pc = operand
			}
		case opPrint:
			value, err := pop()
			if err != nil {
				return nil, err
			}
			output = append(output, value)
		default:
			name := instructionNames[instructionOpcode]
			if name == "" {
				return nil, fmt.Errorf("instruction %d: invalid opcode %d", pc-1, instructionOpcode)
			}
			return nil, fmt.Errorf("instruction %d: VM does not implement %s", pc-1, name)
		}
	}
	return nil, fmt.Errorf("execution exceeded the %d-instruction safety limit", maxExecutionSteps)
}
