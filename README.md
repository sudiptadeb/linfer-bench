# linfer-bench

linfer-bench measures OpenAI-compatible LLM endpoints and compares them,
honestly. Point it at one or more `/v1` URLs, pick the models, and it
measures decode and prefill speed the way an agent experiences them, scores a
tool-calling suite case by case, checks needle-in-haystack retrieval, and
writes a comparison: a terminal table with a short verdict, `report.md`,
`results.json` and a self-contained `report.html`. Runs can be tagged and
compared later, so a backend upgrade or a new quantisation gets a before and
after instead of a feeling.

It is the companion of [linfer](https://github.com/sudiptadeb/linfer), which
starts each local backend with its serving profile and hands this library the
URL. It works just as well against a backend you started by hand, a remote
server, or Ollama, vLLM, llama.cpp and oMLX side by side.

## Install

```sh
go install github.com/sudiptadeb/linfer-bench/cmd/linfer-bench@latest
```

Go 1.22 or later. No dependencies beyond `gopkg.in/yaml.v3`.

## Quick start

```sh
# what does the endpoint serve?
linfer-bench models http://127.0.0.1:8090/v1

# a few minutes against one endpoint; the model is found if it serves only one
linfer-bench run --target llama=http://127.0.0.1:8090/v1 --quick

# two backends on the same model, every suite, the default profile
linfer-bench run --target llama=http://127.0.0.1:8090/v1 \
                 --target mlx=http://127.0.0.1:18080/v1 \
                 --model qwen3-coder:30b --tag before-upgrade

# every model an endpoint offers, speed only
linfer-bench run --target ollama=http://127.0.0.1:11434/v1 --model all --suites speed
```

It prints the request count and a rough estimate before anything runs, a
line per cell and case as it goes, then the table, the verdict and the path
of `report.html`. Output goes to `--out` (default `./linfer-bench-<timestamp>`)
and a copy of `results.json` to the store (see below).

Profiles: `--quick` is one run per cell, 2k context, 1 and 4 streams, 256-token
replies, one pass of the tools; the default is two runs, 2k and 8k, 1/2/4
streams, 384-token replies, two passes; `--full` is three runs, 2k and 32k,
1/2/4/8 streams, 512-token replies, three passes. Any of `--contexts`,
`--concurrency`, `--runs`, `--reply-tokens`, `--tool-passes` and `--suites`
overrides its profile. `--max-context` skips cells the server cannot take;
`--api-key-env NAME` reads a bearer token from the environment; `--parallel`
runs targets at once, which is for remote endpoints: local backends on one
machine contend for its memory.

## With linfer

`linfer bench` lists the backends your machine can run for the configured
model, pauses the serve daemon, starts each backend with exactly the profile
`serve` would use, calls this library against it, stops it, and merges the
rows into one report. Its flags are these flags, and each backend's row is
tagged `backend:<name>`, so

```sh
linfer bench --quick
linfer-bench compare --tags backend:llama,backend:mlx
```

works with no manual tagging.

## A `bench.yaml`

```yaml
targets:
  - name: llama
    url: http://127.0.0.1:8090/v1
    max_context: 131072          # cells past it are skipped, not tried
  - name: mlx
    url: http://127.0.0.1:18080/v1
    models: [all]                # this target: every model it lists
  - name: hosted
    url: https://api.example.com/v1
    models: [big-model]
    api_key_env: EXAMPLE_API_KEY

models: [qwen3-coder:30b]        # for targets without their own list
tags: [after-upgrade]

profile: default                 # quick | default | full
suites: [speed, tools, context]
contexts: [2k, 8k]
concurrency: [1, 2, 4]
runs: 2
reply_tokens: 384
tool_passes: 2
depths: [10, 50, 90]             # needle depths, percent
sampling: {temperature: 0.7, top_p: 0.9}   # pinned on every request; omit for server defaults
request_timeout: 30m
settle: 2s
cases: ./my-cases.yaml           # omit for the built-in suite
out: ./bench-out
```

```sh
linfer-bench run -f bench.yaml
```

Flags given with `-f` win over the file.

## Writing your own cases

The tool suite is a file; the built-in nine cases are
[one such file](bench/cases/builtin.yaml). Yours can be YAML or JSON lines,
with the tools every case is offered and, per case, the messages and what a
right answer looks like:

```yaml
tools:
  - name: get_weather
    description: Get the current weather for a location.
    parameters:
      type: object
      required: [location]
      properties:
        location: {type: string}
        unit: {type: string, enum: [celsius, fahrenheit]}

cases:
  - name: pick the right tool
    messages:
      - {role: user, content: "What's the weather in Paris, in celsius?"}
    expect:
      tools: [get_weather]            # by name, any order, each as many times as listed
      args:
        - {path: location, contains: paris}
        - {path: unit, equals: celsius}

  - name: no call when none is needed
    messages:
      - {role: user, content: "Write a two-line poem. Use no tools."}
    expect: {no_call: true}

  - name: follow-up after a tool result
    messages:
      - {role: user, content: "Weather in Oslo?"}
      - role: assistant
        tool_calls: [{id: c1, name: get_weather, arguments: {location: Oslo}}]
      - {role: tool, tool_call_id: c1, content: '{"temperature": 7}'}
    expect: {no_call: true}
```

An `args` check names a dotted `path` into the arguments (`location.name`,
`attendees.0`) and one of `equals` (type and value: an id given as a string
fails when the model sends a number), `contains` (case-insensitive),
`matches` (a regular expression), `type` or `length`; `tool` restricts it to
the calls of one tool and `any: true` lets one matching call suffice, as for
"weather in Tokyo and in Berlin". Before the checks run, every reply is
scored for valid JSON arguments, the right tools, arguments that fit the
tool's JSON Schema (type, required, properties, enum, items), reasoning or
tool markup leaked into the content, and HTTP errors, which count as
failures. Every failure is one sentence in the report.

```sh
linfer-bench run --target x=http://127.0.0.1:8090/v1 --cases ./my-cases.yaml --suites tools
```

In JSON lines a line is a case, or `{"tools": [...]}` to set the tools for
the cases that follow.

## Tag runs and compare

Every run gets an id (its start time and a short hash) and is kept, as its
`results.json`, in the store at `~/.local/share/linfer-bench/runs/` (override
with `--store`, opt out with `--no-store`), with its tags and the context
it ran in: machine, target URLs, the model ids as served, the server kind and
version where it tells one, the profile and every setting.

```sh
linfer-bench run -f bench.yaml --tag baseline
# ... upgrade the backend, or try multi-token prediction ...
linfer-bench run -f bench.yaml --tag mlx-mtp
linfer-bench run -f bench.yaml --tag mlx-mtp        # a second run under the same tag

linfer-bench runs                                   # id, date, tags, rows, profile
linfer-bench runs --tag mlx-mtp
linfer-bench tag 20261009-143012-9a1b2c nightly     # tags after the fact
linfer-bench untag 20261009-143012-9a1b2c nightly

linfer-bench compare --tags baseline,mlx-mtp
```

Each tag is a column. A tag with several runs is their aggregate: the median
of the runs' medians, the spread across runs, and the run count. Only like is
compared with like: the same context and concurrency cells; a cell one tag
measured and another did not is marked `n/a` and listed, never guessed. The
verdict is one line per metric and cell, the best tag against the next as a
ratio, or `within noise` when the spreads overlap or the gap is under the
noise rule; and it leads with what makes the comparison unfair, when the
runs differ in profile, reply length, sampling, machine or tool suite. The
outputs are the usual: the table, `report.md`, `report.html` with charts per
tag, and `comparison.json`.

`compare` also takes run ids and `results.json` paths: with `--tags` they
join the pool of runs searched for the tags; without, each run's rows become
columns side by side, labelled with the run's time when two share a name.

## What the numbers mean

Each speed cell (a context size × a concurrency) is two passes over new
prompts of that size, all sent at once:

- **cold pass**, one token each: **cold TTFT**, the time to first token with
  every prompt arriving together, and **prefill tok/s**, the prompt tokens
  over that time;
- after a short **settle**, the **warm pass** on the now-cached prompts,
  with a long reply: **decode tok/s per stream**, **combined tok/s** (all
  streams' tokens over the wall time of the pass) and, for one stream, the
  **warm TTFT**, the cache at work as on an agent's next turn.

Medians over the runs, with the min–max in brackets when there was more than
one. The verdict calls a difference only when it is outside both
measurements' own spread and over 5%; with a single run per cell it says so
and asks you to treat small differences as unproven.

Three pitfalls of local backends are built in, each found the hard way:

- **Cold versus warm.** A "combined" figure taken over cold prompts with short
  replies measures how the server queues prefill, not how it decodes: oMLX
  prefills new prompts one at a time, so four cold 2k prompts with 64-token
  replies read 31 tok/s where the same model decoded 112 on warm ones. Hence
  the two passes, and replies of 256–512 tokens.
- **Burst chunks.** Some servers stream bursts of 9–32 tokens per SSE chunk, so
  per-chunk client timing overstates their speed, and a client-side decode
  rate that assumed one token in the first chunk came out 2.6× too high.
  Tokens are counted from the server's `usage`; the first chunk's share is
  estimated from its length at the calibrated characters per token; the
  server's own figures (llama.cpp `timings`, oMLX `usage` rates) are shown
  beside the client's where it reports any, with the stream's **tokens per
  chunk**, and the report says which it has: for Ollama and OpenAI, which
  give token counts only, the client's figure is the only one.
- **Cache commit delay.** oMLX writes a prompt to its cache a moment after
  the request ends; a repeat sent at once misses it (1.8 s to first token
  on a 2k prompt) and one sent a second later hits (0.12 s). The warm pass
  waits `settle` (2 s) after the cold pass.

The **tools** suite is scored per case for valid JSON, the right tool,
schema-valid arguments, correct refusals when no tool is needed, two calls in
one turn, a follow-up after a tool result, an id copied exactly as a string,
nested and integer arguments, leaked markup, and HTTP errors. **Context** is
needle-in-haystack at several depths, exact retrieval or not. Both are
answered by the same server with the same sampling, so they compare backends
on a model, not models.

## A sample report

The terminal, from a quick run of a 0.6B model served by llama.cpp and by
oMLX on one Apple Silicon machine (small on purpose; the numbers are the
format, not a recommendation):

```
metric                         llama                mlx
server                         llama.cpp            oMLX
decode tok/s/stream 2k×1       259.8                348.7
  server-reported              257.7                341.7
  tokens per stream chunk      1.0                  32.0
combined tok/s 2k×1            246.5                299.2
cold ttft s 2k×1               0.18                 0.24
prefill tok/s 2k×1             11761                8592
  server-reported              12754                8885
warm ttft s 2k                 0.06                 0.21
decode tok/s/stream 2k×4       247.3 (247.0–247.5)  174.6 (165.8–178.1)
  server-reported              245.3 (245.0–245.5)  169.4 (153.2–174.4)
  tokens per stream chunk      1.0 (1.0–1.0)        6.6 (6.4–6.7)
combined tok/s 2k×4            463.7                526.5
cold ttft s 2k×4               0.53 (0.33–0.72)     0.68 (0.23–0.94)
prefill tok/s 2k×4             3989 (2921–6296)     3198 (2242–9171)
tools accuracy                 89% (8/9)            100% (9/9)
  valid / right tool / schema  7 / 7 / 7 of 7       7 / 7 / 7 of 7
  no-call correct              2 of 2               2 of 2
  leaks / http errors          0 / 0                0 / 0
needle found                   1 of 1               1 of 1
failures                       0                    0

- fastest single stream (2k context): mlx at 348.7 tok/s, 1.34× llama (259.8)
- most combined output (2k context, 4 streams): mlx at 526.5 tok/s, 1.14× llama (463.7)
- tool accuracy: llama 89% (8/9), mlx 100% (9/9)
- needle retrieved: llama 1/1, mlx 1/1
- single run per cell: no spread to judge noise by, so treat differences under ~10% as unproven
```

Note the oMLX stream's 32 tokens per chunk at one stream, and that its
server-reported decode sits within 2% of the client's figure once the first
chunk is counted by its length rather than as one token.

With colour on a terminal, the clear best of a line is green and failures
are red. `report.html` has the same table, bar charts of single-stream
decode and of combined output per concurrency, sortable tables of every cell,
case and retrieval, the machine and each target's details, in light or dark;
`results.json` (schema 1) holds every number.

## Library

The command is a thin layer over the `bench` package, which an orchestrator
can drive one target at a time:

```go
pairs, err := bench.Expand(ctx, []bench.Target{{Name: "llama", URL: "http://127.0.0.1:8090/v1"}}, nil)
row := bench.RunRow(ctx, pairs[0], bench.Options{Profile: bench.ProfileQuick}, func(e bench.Event) { log.Println(e.Row, e.Suite, e.Text) })
row.Info = append(row.Info, bench.KV{Key: "weights", Value: "31.4 GiB"})
row.Tags = []string{"backend:llama"}
res := bench.Results{Schema: bench.SchemaVersion, Tool: "mine", ID: bench.NewID(time.Now(), "x"), Started: time.Now(), Machine: bench.DetectMachine(), Options: opts, Rows: []bench.Row{row}}
files, err := bench.WriteFiles(dir, res)   // results.json, report.md, report.html
bench.Table(os.Stdout, res, true)
for _, line := range bench.Verdict(res) { fmt.Println("-", line) }
```

`Run` does the same over every pair; `Store` keeps runs by id and tag;
`CompareTags` and `Merge` build comparisons; the context cancels a run
between requests. See the package documentation for the rest.

## Contributing

Issues and pull requests are welcome. Keep tests plain (one set-up, call and
assertion per behaviour; the fake endpoint is in the tests), run `go vet
./...` and `go test ./...` before a pull request, and when a number in the
README changes say what machine you measured on. CI runs vet and test on
Ubuntu and macOS with the current Go.

## License

Apache-2.0. See [LICENSE](LICENSE).
