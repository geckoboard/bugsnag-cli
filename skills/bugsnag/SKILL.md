---
name: bugsnag
description: Read Bugsnag production errors and events from the terminal with the `bugsnag` CLI. Use whenever a task involves a Bugsnag error, investigating a production bug or crash, checking what is failing in a service, reading a stack trace from Bugsnag, or when handed a Bugsnag dashboard URL. This skill covers getting oriented only; the CLI's own `--help` is the reference for flags and per-command caveats.
---

# bugsnag

`bugsnag` reads the Bugsnag Data Access API. It resolves the project from the git
remote, so in a repository with a Bugsnag project start with `bugsnag errors list`.

Run `bugsnag --help` and `<command> --help` for anything not here. Do not rely on
field names, flags or response shapes remembered from elsewhere.

## Handed a dashboard URL? Paste it

```sh
bugsnag view '<url>'                 # quote it: the query string has [ ] and &
bugsnag view '<url>' --all           # every section and the full trace
```

Works from any directory and needs no `--project`. It shows one occurrence if the
URL carries an `event_id`, otherwise the error, otherwise the project's inbox, and
applies any filter state in the URL. The equivalent long-form command is printed to
stderr — use it as the base for the next, more specific command.

## Investigate one error

```sh
bugsnag errors view <error-id>                    # the error + latest stack trace
bugsnag errors view <error-id> --frames full      # library frames as well
bugsnag errors view <error-id> --code             # source, where the notifier sent it
bugsnag errors view <error-id> --all              # stats, trend and the full trace (summaries are shown by default)
```

Frames read `path:line · method`, matching the dashboard. Take the top project
frame and open it in the repository — that is the fastest route from an error to
the code that raised it:

```sh
bugsnag errors view <error-id> | grep -m1 '^1\.'  # the innermost frame
```

Two things to expect while reading a trace:

- The default trace is capped at 12 frames per exception and says so; `--frames
  full` gives the whole thing.
- `--code` shows nothing unless the notifier uploaded source. Server-side Go and
  Ruby services generally do not, so read the repository at that `path:line`
  instead.

### Then the occurrences

An error groups occurrences that can differ. Check whether they do before
concluding one stack trace explains all of them:

```sh
bugsnag errors events <error-id>                  # each occurrence + its message
bugsnag errors event <event-id> --all             # one in full
bugsnag errors event <event-id> --metadata --request
bugsnag errors event <event-id> --breadcrumbs
```

`errors events` prints each occurrence's message under its row, with the facts
that are the same on every occurrence (error class, release stage, and so on)
stated once above the table. Differing messages mean the group spans more than one
failure — read a couple of individual events before deciding on a cause.

## Narrow a list

```sh
bugsnag errors list --release-stage production --since 7d
bugsnag errors list --status '!fixed' --severity error
bugsnag errors list --since 24h --until 1h
bugsnag errors list --list-filters                # what this project supports
bugsnag errors list --limit 100                   # more than a screenful
```

Verified filters: `--status`, `--severity`, `--release-stage`, `--unhandled`,
`--since`, `--until`. A leading `!` negates; repeating a flag means "either";
times take `7d` or an ISO timestamp.

Beyond those six, verify before trusting: the API accepts any filter key with a
200 and ignores the ones it does not act on, so **a filter that does nothing looks
identical to one that matched everything.** Compare a real value against a made-up
one — same count means the field is being ignored.

## Reading the output

Text by default, piped or not. Tables are tab-separated when piped: a header line,
one line per row, nothing truncated. A row's message is the last field, so one
error is one line. `--json` is also available, with the API's values unchanged.

- List endpoints return a bare JSON array. The count and next page are in the
  `X-Total-Count` and `Link` headers, not in a `{"data": …, "total_count": N}`
  envelope.

**Exit codes: `7 <= code <= 9` means retry** (rate limited, server error, network).
Anything else fails the same way again — do not retry. Failures print one stderr
line with `kind`, `exit_code` and `retryable`; stdout stays clean, so a `--json`
pipeline is never corrupted.

## Before you report a number

Read that command's **Caveats** section in `--help`. `errors list`, `errors view`,
`errors events` and `errors event` each have one, and they cover counts that mean
different things, fields the API leaves empty by design, and output omitted rather
than shown as zero.

In particular: `errors view` shows one event count by default. `--stats` adds the
all-time total beside it — the two differ by orders of magnitude, so quote
whichever you mean.

## Project selection

The project is detected from the git remote. When that is wrong, or there is no
remote:

```sh
bugsnag project show                 # which project this repository resolves to
bugsnag project link <project-id>    # pin it when detection is wrong
```
