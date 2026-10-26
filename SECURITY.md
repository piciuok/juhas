# Security policy

## Reporting a vulnerability

Please report security issues privately, through
[GitHub's private vulnerability reporting](https://github.com/piciuok/juhas/security/advisories/new),
not in a public issue. Include what you found, how to reproduce it, and which version (`juhas version`)
and platform you use. You will get an answer within a week.

## What counts

Juhas runs AI agents on pull/merge requests written by other people, so these matter most:

- a way for the review agent to write despite `review.guard: "read-only"`. That means posting to a forge,
  pushing or committing, changing tickets through MCP, or writing files in the clone;
- a way for PR/MR content to make Juhas itself publish something you did not accept;
- access to the local Settings, Panel or proposal pages (127.0.0.1) without the session token;
- a way to tamper with updates, i.e. getting a binary other than the release's onto someone's machine.

Known limits, documented in the [README](README.md#how-a-review-works):

- the guard checks commands and is not a sandbox;
- with Bash and network access for reads, an agent could still send data out in a GET request;
- running a PR's own code (install scripts, tests) is as safe as running that PR locally.

Ideas to reduce these risks are welcome, as issues or pull requests.

## Supported versions

Only the latest release gets fixes. Juhas updates itself; see [Updates](README.md#updates).
