# Contributing to Remolo

Remolo preserves the public commands, wire protocol, token format, and security boundaries of the
original implementation. Compatibility changes need an explicit protocol version or a migration
path. Platform adapters stay behind focused package boundaries and cannot weaken authentication,
path confinement, input limits, or key pinning.

Source, tests, documentation, commit messages, and review text use English and ASCII. Foundation
source follows the Foundation Code Standard. Keep a signature on one line under 100 columns; when
it does not fit, put `(` at the end of the first line, one parameter on each following line, and
`)` on its own line. Comments document contracts, ownership, failures, concurrency, security, or
platform behavior. They do not narrate direct code.

## Development workflow

Use a current Foundation SDK and keep generated outputs under `build/`:

```sh
foundationc package resolve .
foundationc imports --write .
foundationc format --write .
foundationc check .
foundationc lint .
foundationc test .
```

Run the equivalent commands for every package changed by a patch. Protocol tests need positive,
negative, truncated, oversized, and compatibility cases. Network tests need bounded timeouts and
must consume every task. Filesystem tests use a fresh temporary root and cover traversal and
symbolic-link escape attempts. Security-sensitive parsers need arbitrary-input coverage and cannot
panic on peer-controlled bytes.

Use a short Conventional Commit subject such as `feat: encode compatible remolo frames` or
`fix: reject expired connection tokens`. Keep commits subject-only unless a compatibility tradeoff
cannot be read from the diff. Do not add attribution trailers, generated source, local status files,
or development notes.

The repository remains private until the maintainer approves a visibility change. Run the complete
publication scrub before any repository, release, artifact, log, or documentation is shared outside
its current boundary.
