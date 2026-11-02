# Program execution: from source code to a secure running process

This guide connects the compiler's output to the Linux process that runs
it: object files, linking, ELF loading, dynamic libraries, memory mappings,
runtime startup and the security checks around `execve`. It complements
[`compilers`](../compilers/README.md), which explains compiler internals,
and [`linux`](../linux/README.md), which follows the kernel mechanisms
after a process starts.

The path is easiest to remember as two machines doing two jobs: the build
tools transform source into an executable artifact; the kernel and runtime
turn that artifact into a process with an address space, credentials,
threads and open descriptors.

---

## 1. From compiler driver to executable file

**Why.** A source file is not what the CPU executes. Compiler flags,
linker decisions, runtime libraries and target ABI determine which
instructions and dependencies become part of the program. When execution
fails before `main`, the failure is usually in this handoff rather than
in application logic.

**What.** A native build usually produces relocatable object files, then
links them into an ELF executable or shared object. An object file
contains machine code plus symbol and relocation records; the linker
resolves those records and lays out loadable segments. For a dynamic
executable, the ELF program header names an interpreter such as the
dynamic loader (`ld-linux`); a static executable contains its runtime
dependencies directly, subject to toolchain and libc choices.

**How.** The compiler driver orchestrates several distinct stages:

```text
source → preprocessing → parsing/types → IR → optimize → machine code
       → assembler (.o) → linker (ELF) → kernel execve → runtime startup
```

1. The preprocessor expands includes/macros where applicable.
2. The frontend parses syntax, resolves names and checks types, producing
   an AST or intermediate representation.
3. Optimization transforms the IR under language and ABI rules; undefined
   behavior can make a source-level intuition unsafe (see
   [`compilers`](../compilers/README.md)).
4. Code generation selects target instructions and registers; the
   assembler emits an object file with symbols and unresolved references.
5. The static linker combines objects and archives, resolves symbols,
   applies relocations and emits an ELF file. Dynamic symbols may remain
   for the runtime loader to resolve.

ELF has two views. **Sections** such as `.text`, `.rodata`, `.data`,
`.bss`, `.symtab` and `.rela.*` organize build/link information.
**Program headers** such as `PT_LOAD`, `PT_INTERP`, `PT_DYNAMIC` and
`PT_GNU_STACK` describe what the loader maps or interprets at execution
time. The kernel principally follows program headers; a stripped binary
can discard many section names and still run.

```bash
cc -O2 -g -fPIE -pie -Wl,-z,relro,-z,now -o hello hello.c
file hello
readelf -h -l -d -r hello     # ELF header, segments, dependencies, relocations
objdump -d hello              # disassemble executable code
```

The command is an illustrative hardening profile for a common ELF toolchain;
available flags and defaults vary by compiler, linker, architecture and
distribution. Go has its own build/link pipeline and runtime, but the ELF
loader still establishes the process image on ordinary Linux targets.

**Advantages.** Keeping the stages separate makes build failures
diagnosable: unresolved symbols are linker problems, missing shared
objects are loader problems, and illegal instructions are target/CPU
problems. Inspectable artifacts let a release record exactly what was
linked and with which security properties.

**Disadvantages.** Toolchain and ABI versions couple the artifact to its
runtime environment. Dynamic linking moves symbol resolution to startup;
static linking moves more dependency and update responsibility into the
artifact. Optimization can invalidate source-level reasoning when the
program relies on undefined behavior.

---

## 2. `execve`: how Linux replaces a process image

**Why.** Starting a program is not a copy of the executable into RAM.
The kernel must validate the executable format, establish a new virtual
address space, apply credentials and descriptor rules, arrange the
initial stack and choose the first instruction without exposing kernel
memory to user code.

**What.** `execve(path, argv, envp)` replaces the calling process's user
memory image while keeping its process identity (PID) and most process
attributes. It does not create a new process; `fork`/`clone` creates a
task, and `execve` overlays its address space. File descriptors marked
close-on-exec are closed; other open descriptors survive. The executable
format handler for ELF builds mappings from its loadable segments.

**How.** The high-level launch path is:

```text
shell/service → fork/clone → execve
                         → check path, permissions, format and credentials
                         → create new mm/address space
                         → map PT_LOAD segments with page permissions
                         → map PT_INTERP dynamic loader (if present)
                         → build initial stack: argc, argv, envp, auxv
                         → return to user at ELF entry point
                         → loader relocates libraries → program runtime → main
```

The kernel maps file-backed executable pages on demand; the first
instruction fetch can fault pages in from the page cache. It maps the
stack and supplies an auxiliary vector (`auxv`) with items such as page
size, entry point, random bytes and the vDSO address. For a dynamically
linked ELF, control first enters the interpreter named by `PT_INTERP`.
The dynamic loader locates shared objects, maps them, applies relocations,
initializes TLS and constructors, then transfers control to the program's
entry point. C startup code (`_start`) initializes the C runtime and calls
`main`; Go's runtime has its own entry and initialization sequence before
calling `main.main`.

```bash
strace -f -tt -e trace=execve,openat,mmap,mprotect,close ./hello
cat /proc/"$PID"/maps       # virtual address ranges and permissions
cat /proc/"$PID"/smaps_rollup # aggregate RSS/PSS and memory accounting
```

`/proc/PID/maps` shows current VMAs, not which pages are resident; use
`smaps`, `mincore`-style tooling or page-fault counters for residency.
`mmap` does not mean every page has already been read from storage.

**Advantages.** Demand paging lets a large program start without reading
every code/data page. Mapping permissions give the kernel a place to
enforce read/write/execute boundaries. Keeping PID identity across
`execve` makes shell pipelines and service launchers composable.

**Disadvantages.** Startup can fail after the executable is found because
its interpreter or shared library is missing. A binary may map and begin
executing before its first-use pages fault in, so “process started” is not
the same as “all code is resident.” Descriptor leaks across `execve` and
an unexpected loader search path are security and reliability hazards.

---

## 3. Dynamic linking, relocation and the loader trust boundary

**Why.** Shared libraries reduce duplicate disk/RAM use and allow
independent updates, but move part of program construction to runtime.
The loader has to resolve names, select library files, relocate code and
do so under a well-defined search policy.

**What.** The dynamic linker (`ld.so`) reads the executable's dynamic
section, resolves needed shared objects, applies relocations and prepares
the Global Offset Table (GOT) and Procedure Linkage Table (PLT) used for
external symbols. Depending on linker options, PLT entries can be bound
lazily on first call or eagerly at startup. RELRO makes relocation data
read-only after relocation; full RELRO combines this with eager binding.

**How.** The library search path can involve `DT_RPATH`/`DT_RUNPATH`,
`LD_LIBRARY_PATH`, the loader cache and default directories; precise
precedence is documented by the platform loader and differs in edge
cases. For privileged or otherwise secure execution, the loader enters
secure-execution mode and ignores or restricts environment variables
that could inject code, such as `LD_PRELOAD`. `dlopen` extends the same
trust boundary at runtime.

```bash
readelf -d ./hello | grep -E 'NEEDED|RPATH|RUNPATH|BIND_NOW'
ldd ./hello
LD_DEBUG=libs,reloc ./hello       # diagnostic; only run trusted binaries
```

`ldd` is convenient for trusted artifacts, but some implementations or
unusual ELF interpreters can execute code while resolving dependencies;
use `readelf -d` for untrusted files. Do not set `LD_LIBRARY_PATH` to a
directory writable by untrusted users for a privileged service.

**Advantages.** Shared libraries allow security updates without relinking
every consumer, and lazy binding can defer unused symbols. RELRO and
secure-execution rules reduce writable relocation and environment-injection
risk.

**Disadvantages.** Library version drift causes startup or ABI failures.
Lazy binding adds first-call work and leaves PLT state writable until
resolution; eager binding moves that cost to startup. Loader search-path
errors can load an unintended library with the process's full authority.

---

## 4. Execution hardening: what each layer protects

**Why.** A compiler-generated binary runs with the credentials of the
process that starts it. Exploit mitigations make memory corruption harder
to turn into code execution, while kernel policy limits what a compromised
process can reach. No single flag turns an unsafe program into a safe one.

**What.** Build-time properties, kernel memory controls, identity and
syscall policy protect different boundaries:

| Mechanism | Boundary it hardens | What it does not guarantee |
|---|---|---|
| PIE + ASLR | Randomizes executable/library addresses | Does not prevent memory corruption or info leaks |
| NX / non-executable stack | Blocks instruction fetch from data pages | Does not stop code-reuse attacks by itself |
| Stack canary | Detects many stack-overwrite patterns before return | Does not cover all memory corruption or data-only attacks |
| RELRO + eager binding | Makes relocation structures read-only after startup | Does not validate every loaded library's provenance |
| Fortify / bounds checks | Adds checks to selected APIs/operations | Only covers instrumented code paths |
| Capabilities + unprivileged UID | Limits kernel privileges available to the process | Does not restrict every file or network resource by itself |
| Seccomp | Reduces available syscalls | A syscall allowlist is not a substitute for authorization |
| LSM policy (SELinux/AppArmor) | Restricts object access under mandatory policy | Misconfiguration can be permissive or break services |
| Namespaces + cgroups | Isolates views and resource budgets | Not a complete VM boundary against kernel vulnerabilities |

**How.** Start the program with the least privilege it needs; set
`no_new_privs` before executing untrusted code when privilege transitions
must be prevented; apply a narrow syscall filter after required runtime
initialization; and confine filesystem/network access with the platform's
LSM or sandbox. Validate actual binary properties rather than trusting
build flags:

```bash
file ./service
readelf -h -l -d ./service
getcap ./service
grep -E 'NoNewPrivs|Seccomp|Cap(Inh|Prm|Eff|Bnd)' /proc/"$PID"/status
```

For a Go server, keep unsafe pointer use and cgo/native libraries inside
the threat model: compiler mitigations do not sandbox a library executing
in-process. Run parsers and plugins with untrusted input under a separate
identity or process boundary when the impact justifies it. Linux's
namespace, cgroup and capability mechanics are detailed in
[`linux`](../linux/README.md); network-facing controls are in
[`webservices`](../webservices/README.md).

**Advantages.** Layered mitigations reduce exploit reliability and
constrain the impact of a compromise. Artifact inspection and `/proc`
expose what was actually enabled for the running binary.

**Disadvantages.** Mitigations can break compatibility, add startup or
runtime cost and create a false sense of safety when applied without
threat modeling. Seccomp or LSM policy that is too broad has little value;
policy that is too narrow can fail only on rarely exercised code paths.

---

## Further reading

- [`execve(2)`](https://man7.org/linux/man-pages/man2/execve.2.html)
- [`elf(5)`](https://man7.org/linux/man-pages/man5/elf.5.html)
- [`ld.so(8)`](https://man7.org/linux/man-pages/man8/ld.so.8.html)
- [System V ABI](https://gitlab.com/x86-psABIs/x86-64-ABI)
- [Linux kernel security documentation](https://docs.kernel.org/security/)
- [Compiler internals](../compilers/README.md)
- [Linux kernel internals](../linux/README.md)
