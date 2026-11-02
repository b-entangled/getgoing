package minilang

type opcode byte

const (
	opHalt opcode = iota
	opPush
	opLoad
	opStore
	opAdd
	opSub
	opMul
	opDiv
	opNeg
	opGreater
	opEqual
	opJump
	opJumpIfZero
	opPrint
)

const instructionWidth = 9 // one opcode byte followed by one int64 operand

type operandKind uint8

const (
	noOperand operandKind = iota
	integerOperand
	slotOperand
	labelOperand
)

type instruction struct {
	opcode  opcode
	operand operandKind
}

var instructionSet = map[string]instruction{
	"HALT":  {opcode: opHalt},
	"PUSH":  {opcode: opPush, operand: integerOperand},
	"LOAD":  {opcode: opLoad, operand: slotOperand},
	"STORE": {opcode: opStore, operand: slotOperand},
	"ADD":   {opcode: opAdd},
	"SUB":   {opcode: opSub},
	"MUL":   {opcode: opMul},
	"DIV":   {opcode: opDiv},
	"NEG":   {opcode: opNeg},
	"GT":    {opcode: opGreater},
	"EQ":    {opcode: opEqual},
	"JMP":   {opcode: opJump, operand: labelOperand},
	"JZ":    {opcode: opJumpIfZero, operand: labelOperand},
	"PRINT": {opcode: opPrint},
}

var instructionNames = map[opcode]string{
	opHalt:       "HALT",
	opPush:       "PUSH",
	opLoad:       "LOAD",
	opStore:      "STORE",
	opAdd:        "ADD",
	opSub:        "SUB",
	opMul:        "MUL",
	opDiv:        "DIV",
	opNeg:        "NEG",
	opGreater:    "GT",
	opEqual:      "EQ",
	opJump:       "JMP",
	opJumpIfZero: "JZ",
	opPrint:      "PRINT",
}
