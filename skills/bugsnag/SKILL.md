---
name: bugsnag
description: Read Bugsnag production errors and events from the terminal with the `bugsnag` CLI. Use whenever a task involves a Bugsnag error, investigating a production bug or crash, checking what is failing in a service, finding the errors that match a message, symptom or class, checking whether a bug is happening in production or how often, reading a stack trace from Bugsnag, or when handed a Bugsnag dashboard URL. This skill covers getting oriented only; the CLI's own `--help` is the reference for flags and per-command caveats.
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

## Looking for something specific? Search, don't grep

```sh
bugsnag errors list --search 'circular dependency' --since 90d
bugsnag errors list --search SettingsBuilder
```

**Do not fetch a big list and `grep` it.** `--search` is a server-side full-text
filter, and it is both cheaper and stricter than grepping the output:

- One request instead of paging. `--limit 300 | grep` is ten or more requests
  against a 30-per-minute budget, and it still only sees the rows it fetched.
- It matches **across all available data**, including stack frames, which the list
  never prints. Searching one project for `circular` returned six errors where
  grepping the same list could only ever have found four — the other two carried
  the term solely in the frames of their latest event.

Quote a multi-word term. A leading `!` excludes.

## Narrow a list

```sh
bugsnag errors list --release-stage production --since 7d
bugsnag errors list --status '!fixed' --severity error
bugsnag errors list --since 24h --until 1h
bugsnag errors list --list-filters                # what this project supports
bugsnag errors list --limit 100                   # more than a screenful
```

Flags: `--search`, `--status`, `--severity`, `--release-stage`, `--unhandled`,
`--since`, `--until`. A leading `!` negates; repeating a flag means "either";
times take `7d` or an ISO timestamp.

Any other field goes through `--filter`, which takes `field=value`,
`field!=value`, `field>time` or `field<time`:

```sh
bugsnag errors list --filter 'event.class=ActionView::Template::Error'
bugsnag errors list --filter 'event.message=timeout' --filter 'app.context=users#create'
bugsnag errors list --filter 'metaData.Query.widget_key=abc123'   # a custom field
```

`--list-filters` is how you find the field ids; a project's custom fields are its
own and appear in no fixed list. Useful ones that have no flag: `event.class`,
`event.message`, `event.file`, `event.method`, `app.context`, `request.url` — all
substring matches.

Get the id wrong and the API still answers 200, ignores the filter and returns
everything, so **a filter that does nothing looks identical to one that matched
everything.** The CLI catches this when a returned row contradicts the filter, and
prints a `did not filter` warning on stderr — but it can only do that for fields
the rows carry. It cannot check `--search`, `metaData` or user fields, so for
those still confirm a real value against a made-up one: the same count both times
means the field is being ignored.

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

## Last resort: the raw API

The commands above cover error and event work; reach past them only for something
they genuinely do not do, such as releases or teams.

```sh
bugsnag api --list-paths                              # the catalogue
bugsnag api '/projects/{project_id}/releases' --spec  # what that path takes
bugsnag api '/projects/{project_id}/releases' --query per_page=5
```

Quote the path — braces and query strings are shell syntax. `--spec` prints the
endpoint's own YAML, so read it before guessing a parameter. The response is the
API's own shape, unrendered, and none of the caveats above apply to it.
