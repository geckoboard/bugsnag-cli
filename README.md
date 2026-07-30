# bugsnag

A command-line reader for the Bugsnag Data Access API. It works out which project you mean from the git remote, so in a repository with a Bugsnag project you can go straight to:

```sh
bugsnag errors list
```

Read-only: errors, events, projects and organizations. Nothing here writes to Bugsnag.

> **Built for agents first.** The output is designed to be as useful to a coding agent as to a person — one line per error when piped, machine-readable exit codes, and every omission named rather than silent. If you want an agent to use it, point it at [`skills/bugsnag/SKILL.md`](skills/bugsnag/SKILL.md); that'll teach it how and when to use this.

## Install

Needs Go 1.25 or later.

```sh
go install github.com/geckoboard/bugsnag-cli/cmd/bugsnag@latest
```

## First run

Create a Personal Auth Token at <https://app.bugsnag.com/settings/my-account> ("Personal auth tokens"), then:

```sh
bugsnag auth login  # and then enter your token
```

That stores the token and resolves your organization, storing it in `~/.config/bugsnag/config.yaml`.

```sh
bugsnag auth status     # is a token configured, and for which org
bugsnag project show    # which project this repository resolves to
```

Projects are matched automatically based on repository name, with the information cached in the config file.

On a SmartBear-hosted organization, add `--host https://api.bugsnag.smartbear.com` or set the host once in your config file.

## Everyday use

```sh
bugsnag errors list                    # the inbox for this repository's project
bugsnag errors list --search 'foo'     # show only errors that match
bugsnag errors view <error-id>         # the error, and its latest stack trace
bugsnag errors events <error-id>       # that error's occurrences
bugsnag errors event <event-id>        # one occurrence in full
```

### Handed a dashboard URL?

Paste it. This is the fastest way to pick up someone else's investigation, and it needs no repository and no `--project`:

```sh
bugsnag view 'https://app.bugsnag.com/example-org/example-api/errors/60cb09e86dc3a70007391ba2'
```

`view` works out what the URL names — it works on inboxes, errors and individual error events. Make sure to **Quote the URL**: its query string often contains `[`, `]` and `&`.

### Filtering

```sh
bugsnag errors list --search 'circular dependency'   # full-text, across every field
bugsnag errors list --release-stage production --since 7d
bugsnag errors list --filter 'event.class=TypeError' # any field id, including custom ones
bugsnag errors list --list-filters                   # what this project can be filtered on
```

## Output

Text by default, whether or not you are on a terminal:

- **On a terminal** — gridlines, colour, and columns fitted to the width.
- **Piped** — tab-separated. A header line, one line per row, nothing padded and nothing truncated.
- **`--json`** — the API's own JSON values, unchanged. Not a re-marshal, so large integers, unknown fields and key order all survive exactly as the API sent them. It gets pretty-printed, and a multi-page result is concatenated into one array.

`--json` is never redacted; the text path masks values whose key looks like a credential.

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

**`7 <= code <= 9` means retry.** Everything else will fail the same way again until something changes.

## Development

See the [`Makefile`](./Makefile) for tasks.

`internal/bugsnagapi/client.gen.go` is generated from the vendored spec in `api/openapi/` plus `overlay.yaml`, and must not be hand-edited — the overlay is where every deviation from the spec lives, applied strictly so a spec refresh that makes a patch a no-op fails the build rather than silently dropping it.

## License

MIT. See [`LICENSE`](./LICENSE).
