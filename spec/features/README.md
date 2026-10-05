---
format: https://specscore.md/features-index-specification
---

# Features

Feature specifications for this project.

## Index

| Feature | Status | Description |
|---------|--------|-------------|
| [Plans and AI metering](plans-and-ai-metering/README.md) | Draft | The contract between the server side of DataTug plans and AI metering and every client that shows it: the records that hold a plan and a month's count, the endpoint that gives a client the plan and what is left, which account pays for a question, the models a payer may choose from and what each costs in questions, the refusal at a limit, the public count of Founding places and the request that starts a purchase. Fixtures in `testdata/contract/`, pinned by `CHECKSUMS`, give one example of each shape. No price, allowance, count of places or money ceiling is written here: a number is set by configuration and a client reads it from a response. |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/features-index-specification*
