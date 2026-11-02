# Build a tiny DSL, compiler and assembler in Go

The [compiler reference](README.md) explains the stages used by production
toolchains. This companion builds a small language end to end in Go: source
text becomes tokens, an AST, readable stack assembly, bytecode and finally
output from a virtual machine. The implementation lives in
[`compilers/minilang/`](https://github.com/b-entangled/getgoing/tree/main/compilers/minilang)
and has no third-party dependencies.

This is a teaching compiler with its own **virtual-machine instruction
set**. Its assembler does not emit x86-64, ARM or a native object file. A
native assembler also needs a real ISA's encodings, ABI and object-file
relocations; this project makes the language and assembly pipeline small
enough to understand and test.

## 1. Pick a tiny language before writing the compiler

The example DSL has signed 64-bit integer values, program-wide variables,
arithmetic, comparisons, `while`, and `print`:

```text
// Compute a factorial.
let n = 5;
let result = 1;
while n > 1 {
    result = result * n;
    n = n - 1;
}
print result;
```

The language deliberately has no functions, strings, user-defined types,
block scopes or I/O beyond `print`. A small explicit feature set makes the
grammar, compiler and assembler concrete instead of burying the central
ideas in a large language specification.

Its grammar is:

```text
program      = { statement } EOF
statement    = "let" identifier "=" expression ";"
             | identifier "=" expression ";"
             | "print" expression ";"
             | "while" expression block
block        = "{" { statement } "}"
expression   = comparison
comparison   = additive [ (">" | "==") additive ]
additive     = multiply { ("+" | "-") multiply }
multiply     = unary { ("*" | "/") unary }
unary        = "-" unary | primary
primary      = integer | identifier | "(" expression ")"
```

Declarations are unique and variables must be declared before use.
Variables share one program-wide namespace, including declarations inside
a `while` body. VM slots start at zero; a declaration in a loop body
assigns its initializer each time that body executes. Statements require
semicolons except the braced `while` body. These are language-design
decisions; scopes, functions and richer types can be added later.

## 2. Use a pipeline with explicit intermediate forms

```text
DSL source
  → lexer: tokens with line/column positions
  → recursive-descent parser: expression/statement AST
  → compiler: name checks and stack-assembly emission
  → assembler: opcode/operand validation and label resolution
  → bytecode: fixed-width VM instructions
  → VM: stack operations, variables, branches and print output
```

Each phase has one job and an inspectable input/output. This is valuable
for debugging: if `2 + 3 * 4` computes incorrectly, inspect tokens, the
tree, emitted assembly and bytecode in that order.

### Lexing: characters become tokens

The lexer in `lexer.go` tracks a rune position and source line/column. It
recognizes identifiers, decimal integers, keywords, punctuation and
operators; whitespace and `//` comments are discarded. It does not decide
whether a variable exists or whether an expression is legal in a
statement—that belongs to later phases.

The token stream for the two declarations is approximately:

```text
LET IDENT(n) = INTEGER(5) ;
LET IDENT(result) = INTEGER(1) ;
```

An unexpected character or an integer outside `int64` is reported with a
line and column instead of being silently accepted.

### Parsing: tokens become a tree

`parser.go` uses one recursive-descent function per grammar level. The
call chain `parseExpression → parseComparison → parseAdditive →
parseMultiplicative → parseUnary → parsePrimary` encodes precedence:
multiplication binds tighter than addition, and parentheses override the
default. The loops in additive and multiplicative make those operators
left-associative.

For `2 + 3 * 4`, the syntax tree is shown with the emitted operations:

```text
       ADD
      /   \
   PUSH2   MUL
          /   \
       PUSH3  PUSH4
```

The implementation has distinct AST nodes for integer, variable, unary,
binary, declaration, assignment, print and while expressions/statements.
Keeping syntax in a tree lets the compiler handle meaning without
re-parsing source text.

### Semantic checks and code generation

`compiler.go` walks the AST and maintains a symbol table mapping variable
names to numbered VM slots. It rejects duplicate declarations, unknown
reads and assignments before declaration. Since this language has only
one value type (`int64`), it needs no multi-type inference pass.

Expressions compile to **postfix stack operations**. Each operand pushes
a value; an operator pops its operands and pushes its result:

```text
source:     2 + 3 * 4
assembly:   PUSH 2
            PUSH 3
            PUSH 4
            MUL
            ADD
```

The `while` statement illustrates control-flow lowering. The compiler
creates two labels, tests the condition, branches to the end when it is
zero, and jumps back after the body:

```text
L0:
    ... condition ...
    JZ L1
    ... body ...
    JMP L0
L1:
```

Comparisons leave `1` for true and `0` for false. `JZ` pops its condition
and jumps when it is zero.

## 3. Define an assembly language and encode it

The custom assembly has a compact instruction set:

| Instruction | Effect |
|---|---|
| `PUSH n` | Push signed integer literal `n` |
| `LOAD slot` / `STORE slot` | Read/write a variable slot |
| `ADD`, `SUB`, `MUL`, `DIV` | Pop right and left operands; push the result |
| `NEG` | Negate the top stack value |
| `GT`, `EQ` | Compare two values; push `1` or `0` |
| `JMP label`, `JZ label` | Unconditional / zero-conditional branch |
| `PRINT` | Pop a value into the program's output |
| `HALT` | Stop execution |

The factorial compiler output is:

```text
PUSH 5
STORE 0
PUSH 1
STORE 1
L0:
LOAD 0
PUSH 1
GT
JZ L1
LOAD 1
LOAD 0
MUL
STORE 1
LOAD 0
PUSH 1
SUB
STORE 0
JMP L0
L1:
LOAD 1
PRINT
HALT
```

`assembler.go` is a two-pass assembler. The first pass validates
instructions and records each label's instruction index. The second checks
operands, resolves forward and backward branches, then encodes every
instruction. All instructions occupy nine bytes: one opcode followed by a
little-endian signed 64-bit operand. Instructions without an operand store
zero in that field. Branch operands are instruction indices, not byte
offsets; fixed-width instructions make that distinction easy to compute.

The assembler is also callable directly, which lets you experiment with its
instruction set independently of the DSL compiler:

```go
assembly := "PUSH 6\nPUSH 7\nMUL\nPRINT\nHALT\n"
bytecode, err := minilang.Assemble(assembly)
if err != nil {
    log.Fatal(err)
}
output, err := minilang.Run(bytecode, 0)
if err != nil {
    log.Fatal(err)
}
fmt.Println(output) // [42]
```

This format is intentionally simple. A native assembler has variable-size
instructions and more involved relocation, symbol, section and object-file
rules. Even a production compiler's assembler-like output is not always
machine code: it may still need register allocation, instruction
selection, linking and relocation.

## 4. Execute bytecode with a small virtual machine

`vm.go` decodes the opcode and operand, then interprets the instruction.
The VM has an operand stack and a fixed array of integer variable slots.
For `MUL`, it pops the right value, pops the left value, multiplies and
pushes the result. The instruction pointer is measured in instructions;
`HALT` returns the values emitted by `PRINT`.

The example interpreter also checks malformed bytecode, invalid jump and
variable addresses, stack underflow and division by zero. A step limit
stops accidental infinite loops. These checks are useful teaching and
testing boundaries; a production VM may choose different resource limits
and error semantics.

## 5. Compile and run it from Go

The public API is small: compile source, inspect the assembly, then run the
resulting bytecode with the number of declared variable slots.

```go
package main

import (
    "fmt"
    "log"

    "github.com/b-entangled/projects/learnings/compilers/minilang"
)

func main() {
    source := `
        let n = 5;
        let result = 1;
        while n > 1 {
            result = result * n;
            n = n - 1;
        }
        print result;
    `

    program, err := minilang.Compile(source)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Assembly:")
    fmt.Print(program.Assembly)

    output, err := minilang.Run(program.Bytecode, len(program.Variables))
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Output:", output)
}
```

Run the compiler and its tests from the repository root:

```sh
go test ./compilers/minilang
```

Expected final output is `Output: [120]`. The tests verify expression
precedence, the factorial loop, variable errors, assembler label
resolution, malformed assembly and VM runtime errors. They are executable
specifications for the grammar and bytecode behavior.

## 6. How to grow the language

Extend one layer at a time, keeping tests at each boundary:

1. Add a token and lexer test for each new operator or keyword.
2. Add a grammar production, AST node and parser tests.
3. Define name/type/scope behavior before emitting code for it.
4. Add VM instructions and assembler encodings, including validation.
5. Lower the new AST node to instructions and test execution end to end.

Good next features are `if/else`, lexical block scopes, functions/calls,
booleans, strings and arrays. As the language grows, introduce a typed
intermediate representation rather than making every backend understand
surface syntax. For a native backend, choose a target ISA and ABI, define
calling conventions and object format, and use a mature linker/toolchain
where possible instead of calling custom VM bytecode “native assembly.”

## Related reading

- [Compiler pipeline and internals](README.md)
- [Lexer/parser, semantic analysis and IR chapters](README.md#2-lexing-and-parsing-tokens-grammar-and-the-first-honest-tree)
- [Executable implementation and tests](https://github.com/b-entangled/getgoing/tree/main/compilers/minilang)
