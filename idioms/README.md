# Construction Idioms (Go-native)

Two ways to build a configured value. Read them together: they are the two
halves of the same decision.

```bash
go test -race ./idioms/...
```

---

## 27. Functional Options — [`options/`](options)

**Why.** A library constructor starts with three parameters and ends with
twelve, and each new optional one breaks every caller — boolean flags and
giant config structs trade clarity for compatibility. The need is a
constructor that grows without breaking callers, where every option is a
function that can validate itself.

**Intent.** Give callers many *optional* settings without a positional
parameter list of booleans.

**Go shape.** `Option` is a function that mutates an unexported config;
fallible options return `error`.

```go
c, err := options.New(
    options.WithName("svc"),
    options.WithEndpoint("a.b:1"),
    options.WithTimeout(5*time.Second),
    options.WithHeader("X-T", "on"),
)
```

**Use when.** Most fields are optional, options must validate themselves,
or you want composable presets (`options.Production(cfg)` applied as
`options.New(name, options.Production)`).

**Do not use when.** The caller must be able to see, diff, or unmarshal the
settings — use [`config`](config) instead.

**The Go-specific decision.** Two details that separate working code from
blog-post code:

1. **Defaults are applied before options run.** Because each option is a
   closure over a config already filled with defaults, *any* option order
   produces the same result — `New(WithA, WithB)` ≡ `New(WithB, WithA)`.
   If defaults were applied after, a default would clobber an option.
2. **Options can fail.** `Option` is `func(*Config) error` rather than
   `func(*Config)`, so a bad header name or an out-of-range timeout is
   reported by `New` instead of being silently accepted. Presets are
   ordinary options, so a caller can layer them.

`Production` / `Development` are `Preset` values — the case the pattern
exists for: named bundles of defaults with no builder class. `Preset.Then(...)`
layers more options on top without mutating the preset, and
`Named("production")` selects a preset by string (returning an error rather
than a silent default). `Dial(opts...)` shows the pattern at its natural
boundary: a constructor that takes options and returns a ready client.

Name and endpoint deliberately have **no default** — a default would make
"required" a lie — so `New` validates after applying options, once, for
cross-field invariants no single option can see (`TestMissingRequiredFieldReportedOnce`).

**Walkthrough.** A client grows from two settings to six without a single
caller recompiling differently — and a bad value fails loudly.

```go
cfg, err := options.New(
    options.WithName("svc"),
    options.WithEndpoint("a.b:1"),
    options.WithTimeout(5*time.Second),
    options.WithHeader("X-Trace", "on"),
)
if err != nil { ... } // the failing option, named with its index

options.Dial(
    options.WithName("svc"),
    options.WithEndpoint("a.b:1"),
) // a constructor that takes options and returns a ready client
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestOptionsOverrideDefaults` | an option always beats the default |
| `TestOptionsAreOrderIndependent` | permutation of independent options → identical config |
| `TestInvalidOptionIsRejectedWithPosition` | `ErrInvalidOption` names the failing option index |
| `TestHeaderAccumulatesAcrossOptions` | dependent options (header then header) follow written order |
| `TestMissingRequiredFieldReportedOnce` | `New` reports the missing required fields together |
| `TestConfigIsEffectivelyImmutable` | no setters: configuration is decided at construction |

---

## 28. Option-Struct Config — [`config/`](config)

**Why.** Application settings arrive from YAML files, flags and environment
overrides, and operators need to print them, diff them and see every error
at once — none of which an opaque chain of closures supports. The need is
plain data the standard library can decode, inspect and validate.

**Intent.** The deliberately plain alternative: one **exported struct** of
settings.

```go
s := config.Settings{Name: "svc", Endpoint: "a.b:1", Timeout: config.Duration(5 * time.Second)}
c, err := config.New(s)                // validates, then freezes a copy
```

**Use when.**

- the config must be **unmarshalled** from YAML/JSON — options cannot be
  decoded into, and exported fields with tags are required anyway;
- most fields are required, so "optional" buys nothing;
- you need to **diff two configurations**, print them, or pass them around
  as a value;
- you want to report **all** validation errors at once.

**Do not use when.** You have many genuinely optional fields and options
that validate themselves — that is [`options`](options).

**The Go-specific decision.** The trade-off in one line: the struct exposes
*what* the settings are; the options idiom encodes *how much choice the
caller has*.

The package shows the three things a struct makes easy and options make
awkward:

1. **Layered configuration** — `Default()` → `FromJSON(data)` overlays a
   file onto defaults, and `Patch(base, overlay)` applies only the keys
   present in a partial document (merged via a `map[string]RawMessage`, so
   absent keys are untouched).
2. **Batch validation** — `validate()` returns `errors.Join` of every
   problem, reachable with `errors.Is` against `ErrMissingField` /
   `ErrOutOfRange`.
3. **Introspection** — `Diff(a, b)` and `String()` work because the fields
   are readable. An opaque chain of closures has nothing to compare.

`Duration` is the one custom type a settings struct usually needs: it lets
a config file say `"150ms"` while `MarshalJSON` emits the same readable
form. `New` **copies** the input (deep-copying `Headers`), so a caller
mutating its `Settings` afterwards cannot change a live `Config`, and
`Must` is provided for static/test configuration where an error return
would be ignored.

**Walkthrough.** Settings arrive as a file, overlay defaults, validate in
one pass — and can be diffed because every field is readable.

```go
base := config.Default()
cfg, err := config.FromJSON(data) // file overlays defaults; absent keys survive
if err != nil {
    // errors.Is picks every problem: ErrMissingField, ErrOutOfRange
}

changed := config.Diff(base, cfg.Settings()) // options have nothing to compare
_ = changed
```

**Proving tests.**

| Test | Property |
|---|---|
| `TestValidationReportsEveryProblemAtOnce` | one pass, all four errors |
| `TestFromJSONUsesDefaultsThenOverrides` | file overlays defaults, absent keys survive |
| `TestPatchOverlaysOnlyPresentFields` | partial documents merge correctly |
| `TestConfigDoesNotAliasCallerSettings` | copy-in, no leak on later mutation |
| `TestDiffListsChangedFields` | introspection that options cannot offer |
| `TestDurationAcceptsNanoseconds` | both `"1s"` and `1000000000` decode |

---

## Choosing between them

|  | `options` | `config` |
|---|---|---|
| Field visibility | unexported | exported + tags |
| Required fields | mixed in with optional ones | natural, validated up front |
| From a file / flags | needs bespoke mapping | `json`/`yaml` for free |
| Diff / print / compare | not possible | `Diff`, `String` |
| Report all errors at once | first failure wins | `errors.Join` |
| Composable presets | yes (`Production` as an option) | as a `Default()`-style function |
| Best for | libraries, ~3+ optional params | app settings, CLI flags, config files |
