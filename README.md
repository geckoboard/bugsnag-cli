# bugsnag

A command-line reader for the Bugsnag Data Access API. It works out which project
you mean from the git remote, so in a repository with a Bugsnag project you can go
straight to:

```sh
bugsnag errors list
```

Read-only: errors, events, projects and organizations. Nothing here writes to
Bugsnag.

> **Built for agents first.** The output is designed to be as useful to a coding
> agent as to a person — one line per error when piped, machine-readable exit
> codes, and every omission named rather than silent. If you want an agent to use
> it, point it at [`skills/bugsnag/SKILL.md`](skills/bugsnag/SKILL.md); that file
> is the agent-facing entry point and is deliberately short, because the CLI's own
> `--help` is the reference.

## Install

Needs Go 1.25 or later.

```sh
go install github.com/geckoboard/bugsnag-cli/cmd/bugsnag@latest
```

Or from a checkout:

```sh
go install ./cmd/bugsnag             # installs `bugsnag` into $(go env GOPATH)/bin
```

The version is read from the build info, so `bugsnag version` reports the tag of
a versioned install and the git revision of a build from a checkout — no build
flags needed.

## First run

Create a Personal Auth Token at
<https://app.bugsnag.com/settings/my-account> ("Personal auth tokens"), then:

```sh
bugsnag auth login --token <token>
bugsnag auth login                     # prompts, or reads the token from stdin
```

That stores the token and resolves your organization in one request — listing the
organizations both proves the token works and produces the id every other endpoint
needs. It lands in `~/.config/bugsnag/config.yaml` (or
`$XDG_CONFIG_HOME/bugsnag/config.yaml`), written `0600`. If the file is ever left
readable by anyone else you get a warning, since it holds a live token.

```sh
bugsnag auth status                    # is a token configured, and for which org
bugsnag project show                   # which project this repository resolves to
```

On a SmartBear-hosted organization, add `--host https://api.bugsnag.smartbear.com`
or set the host once in your config file.

## Everyday use

```sh
bugsnag errors list                    # the inbox for this repository's project
bugsnag errors view <error-id>         # the error, and its latest stack trace
bugsnag errors events <error-id>       # that error's occurrences
bugsnag errors event <event-id>        # one occurrence in full
```

`errors list` gives a row of facts per error with the message beneath it, because
on a service where every error shares one context the message is the only thing
that tells two rows apart. `--all` on either detail command adds every optional
section and the full trace.

A page is sized to your terminal, so the top of the answer does not scroll away
before you read it. `--limit` sets how many to show (pages are fetched behind it
until the limit is met), and `--all-pages` lifts the limit and fetches everything.

### Handed a dashboard URL?

Paste it. This is the fastest way to pick up someone else's investigation, and
it needs no repository and no `--project`:

```sh
bugsnag view 'https://app.bugsnag.com/example-org/example-api/errors/60cb09e86dc3a70007391ba2'
```

`view` works out what the URL names — one occurrence if it carries an `event_id`,
otherwise the error, or a project's inbox if it has no error id — and applies any
filter state in it. **Quote the URL**: its query string contains `[`, `]` and `&`.
The equivalent long-form command is printed to stderr, so the next, more specific
step is written out for you.

### Filtering

```sh
bugsnag errors list --release-stage production --since 7d
bugsnag errors list --list-filters     # what this project can be filtered on
```

`--list-filters` lists what a project can be filtered on. Every command that reads
error or event data also carries a **Caveats** section near the top of its
`--help`, and those are load-bearing rather than pedantry: they cover counts that
mean different things, fields the API leaves empty where you would expect a value,
and output that is deliberately omitted rather than shown as zero.

## Output

Text by default, whether or not you are on a terminal:

- **On a terminal** — gridlines, colour, and columns fitted to the width.
- **Piped** — tab-separated. A header line, one line per row, nothing padded and
  nothing truncated.
- **`--json`** — the API's own JSON values, unchanged. Not a re-marshal, so large
  integers, unknown fields and key order all survive exactly as the API sent them.
  It is pretty-printed, and a multi-page result is concatenated into one array, so
  it is not a byte-for-byte copy of a `curl`.

`--json` is never redacted; the text path masks values whose key looks like a
credential, because `metaData` and request headers do carry live secrets.
`--no-redact` turns that off. `NO_COLOR` is honoured, and `--time` selects
`auto`, `relative`, `local` or `raw` timestamps.

## Exit codes

Meaningful, and fixed — they are the contract with anything scripting this.

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | internal error |
| 2 | usage error |
| 3 | configuration |
| 4 | authentication |
| 5 | not found |
| 6 | bad request |
| **7** | **rate limited** |
| **8** | **server error** |
| **9** | **network failure** |
| 10 | cancelled |
| 11 | untrusted host |
| 12 | decode failure |

**`7 <= code <= 9` means retry.** Everything else will fail the same way again
until something changes. Failures print one line to stderr carrying `kind`,
`exit_code` and `retryable`, and stdout stays clean, so a `--json` pipeline is
never corrupted by an error message.

## Environment

Settings come from flags and the config file, in that order; there are no
per-setting environment variables. Two standard variables are honoured for
locating the config and for colour:

| Variable | Sets |
|---|---|
| `XDG_CONFIG_HOME` / `HOME` | where the config file lives |
| `NO_COLOR` | disables colour |

## Development

```sh
make test              # go test -race -shuffle=on ./...
make lint              # go vet + staticcheck
make verify-codegen    # regenerate the API client and fail on any diff
make update-spec       # refresh the vendored OpenAPI spec
```

`internal/bugsnagapi/client.gen.go` is generated from the vendored spec in
`api/openapi/` plus `overlay.yaml`, and must not be hand-edited — the overlay is
where every deviation from the spec lives, applied strictly so a spec refresh that
makes a patch a no-op fails the build rather than silently dropping it.

The suite runs with `-race` and `-shuffle=on`. It is fast because the retry tests
inject a no-op sleep rather than waiting out real backoff.
