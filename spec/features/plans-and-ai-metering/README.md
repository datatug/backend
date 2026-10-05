---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Plans and AI metering

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

The contract between the server side of DataTug plans and AI metering and every client that shows it: the records that hold a plan and a month's count, the endpoint that gives a client the plan and what is left, which account pays for a question, the refusal at a limit, the public count of Founding places and the request that starts a purchase. Fixtures in `testdata/contract/`, pinned by `CHECKSUMS`, give one example of each shape. No price, allowance, count of places or money ceiling is written here: a number is set by configuration and a client reads it from a response.

## Problem

A client (the web app, the CLI) has to show three things: which plan an account is on, how many AI questions are left this month, and, at a limit, why a question was refused and what the person can do. It must do so without asking the server team a question and without guessing a number. Without one written contract, each client reads the records its own way, computes `left` its own way, and parses a refusal sentence. A client that copies a number into its code is wrong the day the number changes.

This page fixes the shapes. The fixtures are copied into each client's repository together with `CHECKSUMS` and the test `contract4datatug/contract_test.go` of this repository, which is standard-library only; each copy is checked against the same digests, so a copy cannot drift unnoticed.

## Behavior

### Words used

- **Account**: the thing that holds a plan. An account is a Sneat space, and `accountId` is the ID of that space. A **personal account** is a person's personal space. An **organisation account** is a space of type `company`. Membership and roles are the space's own; this contract stores no owner or member list.
- **Plan**: one of `free`, `pro`, `team`, `business`, `company`, `enterprise`. A personal account is on `free` or `pro`. An organisation account is on `team`, `business` or `company`. `enterprise` is reserved; it is not sold through checkout. A client must show a plan it does not know by its name and must not fail on it.
- **Question**: what the allowance counts, defined in [REQ:question](#req-question).
- **Payer**: the account whose allowance a question is counted against.
- **Buyer**: the signed-in person who pays for a plan.
- **Place**: one of the limited places of the Founding offer ([REQ:offer-endpoint](#req-offer-endpoint)).
- **Own key**: an AI key the person supplies. A question answered with it never reaches the server and is never counted or limited. Every refusal says so.
- **UTC**: every instant is an RFC 3339 timestamp in UTC, and every month and day boundary is a UTC one.

Any field a client does not know is ignored, and any enum value a client does not know is treated as the safest reading named below: an unknown `plan` is shown by name, an unknown `status` is treated as `none`, an unknown `blocked` or `reason` is treated as "no question can be asked now" and the response's own sentence is shown.

### Records

Two documents per account, written by the server only. The plan changes on a money event (rare); the count changes on every question; they are separate so the two writers never contend.

#### REQ: plan-record

The plan of an account is the document `spaces/{accountId}/ext/datatug/plan/current`, of version 1:

| Field | Type | Meaning |
|---|---|---|
| `v` | number | `1`. A reader that meets another value treats the account as Free. |
| `plan` | string | The plan the record states. |
| `status` | string | `none`, `active`, `trialing`, `past_due`, `ended`. |
| `period` | string | `none`, `month`, `year`: what the account pays for. This is the billing period only; the AI count is always per calendar month ([REQ:usage-record](#req-usage-record)). |
| `paidUntil` | timestamp, optional | The end of the last period that was paid for. Set from a paid invoice only; it never moves on because a new period began unpaid. |
| `endsAt` | timestamp, optional | Present when a cancellation is scheduled: the plan ends then. |
| `endedReason` | string, optional | `canceled`, `unpaid`, `refunded`, `paused`. Present only when `status` is `ended`. |
| `founding` | boolean | The subscription carries the Founding discount. |
| `limits` | object | Absent when `plan` is `free`. See below. |
| `limits.contributors` | number | The cap on contributors. |
| `limits.projectGuests` | number | People besides the owner in each project of a personal account. |
| `limits.aiQuestions` | number | Included AI questions per calendar month. `0` means none, never "no limit". |
| `limits.aiModelClasses` | array of string | The model classes the plan includes. `fast` is the only class a client can rely on; others may be added, and a client tolerates an unknown class. |
| `limits.aiPaysFor` | string | `owner` (the allowance is the owner's alone) or `contributors` (one pool for the contributors of the organisation). |
| `aiExtraQuestions` | number | Extra capacity on top of `limits.aiQuestions`. |
| `updatedAt` | timestamp | |

The record holds no payment-provider identifier, no amount, no email and no member list. A missing document, or `plan: free`, is the Free plan. The Free numbers are configuration: a reader takes them from `ai` and `limits` of the endpoint ([REQ:plan-endpoint-response](#req-plan-endpoint-response)), never from a constant and never from a document.

Fixtures: `plan-record-pro-active.json`, `plan-record-team-cancel-scheduled.json`, `plan-record-pro-past-due.json`, `plan-record-ended-refunded.json`.

#### REQ: usage-record

The count of an account for one month is the document `spaces/{accountId}/ext/datatug/aiUsage/{periodId}`, where `periodId` is `YYYY-MM` of the UTC month, of version 1:

| Field | Type | Meaning |
|---|---|---|
| `v` | number | `1` |
| `periodId` | string | |
| `used` | number | Questions used, each counted by its weight ([REQ:models](#req-models)). |
| `questions` | number | Questions asked, unweighted. |
| `byMember` | map of user ID to number | Use per member of an organisation. Optional: a reader tolerates its absence. |
| `capped` | boolean, optional | True when the server has stopped included AI for this account for the rest of the month because of what the questions cost. A reader treats it as nothing left. |
| `resetsAt` | timestamp | The first instant of the next UTC month. |
| `updatedAt` | timestamp | |

A reader computes `left = capped ? 0 : max(0, limits.aiQuestions + aiExtraQuestions - used)`, with a missing count document meaning `used: 0`. The count starts again at the first instant of each UTC month, for every plan and every billing period; no job runs, a new document simply begins at 0. A change of plan moves no counter: `used` stays, and the new limit applies at once.

Fixtures: `ai-usage-record.json`, `ai-usage-record-capped.json`.

#### REQ: test-record

A purchase made in the provider's test mode never writes `plan/current` in production. It writes the same fields to `spaces/{accountId}/ext/datatug/plan/test`, a shadow record. The server's meter and the endpoint never read it, and a client must not read it either: it exists so that a test purchase can be looked at.

#### REQ: who-may-read

Every member listed in the space's `userIDs` may read the two records of the account through the space's existing reading rule. On a personal account that is the owner alone. A person who works in a project without being a member of the account's space cannot read it and does not need to: their own personal account pays for them ([REQ:payer-rules](#req-payer-rules)).

#### REQ: direct-readers

A client may listen to the two documents for live updates, but must use them only to notice a change:

1. Call `GET /v0/datatug/plan` first, again whenever the account in view changes, and again at `ai.resetsAt`.
2. Then it may listen to `plan/current` and to `aiUsage/{ai.periodId}` of that account. The month in the document ID comes from the server's `ai.periodId`, never from the browser's clock.
3. When a document changes, call the endpoint again and show what it says. The server applies a rule the documents do not show ([REQ:stale-rule](#req-stale-rule)), and the endpoint also reports the day ceiling, an exhausted free budget and a sign-in that is not trusted as `ai.blocked`; a screen built from the documents alone can show a plan and a number the server will refuse.

#### REQ: stale-rule

A plan holds only while it is paid for. The server treats a record as Free when the current time is later than `paidUntil` plus a grace, whose length is set by configuration and is longer for `past_due` than for `active` and `trialing`. A paying `status` with no `paidUntil` is Free. The endpoint reports the result as `effectivePlan` ([REQ:plan-endpoint-response](#req-plan-endpoint-response)); a client shows `effectivePlan` and the effective `limits`, and never recomputes the grace.

Fixture: `plan-response-stale.json` (the record says `pro` and `past_due`; the plan in force is `free`).

#### REQ: fail-towards-free

A record that cannot be read, a version the server does not know, a status nobody mapped, or a missing number gives the person the Free allowance or a clear refusal, never an unlimited one. Where the count cannot be read or written, the server answers 503 with `error.code` `upstream` and sends nothing to a model ([REQ:refusal-body](#req-refusal-body)).

### What a provider's state means for the record

#### REQ: state-table

The record follows the state of the subscription at the payment provider through one pure function of plain facts. The facts are:

| Fact | Type | Meaning |
|---|---|---|
| `accountKind` | `personal` or `organisation` | The kind of the account the subscription belongs to. |
| `tier` | string | The tier of the current price: `pro`, `team`, `business`, `company`; empty when the price is not one the catalogue knows. |
| `period` | `month` or `year` | The billing interval. |
| `providerStatus` | string | The provider's subscription status: `active`, `trialing`, `past_due`, `unpaid`, `canceled`, `paused`, `incomplete`, `incomplete_expired`. |
| `paidUntil` | timestamp, optional | The end of the last paid period. |
| `endsAt` | timestamp, optional | A scheduled cancellation. |
| `lastInvoiceRefundedInFull` | boolean | Whether the last paid invoice was refunded in full. |
| `refundedAt` | timestamp, optional | When. |
| `firstPaidAt` | timestamp, optional | The time of the first payment. |
| `founding` | boolean | The Founding discount is on the subscription. |
| `grants` | object | The limits the plan allows: `contributors`, `projectGuests`, `aiQuestions`, `aiModelClasses`, `aiPaysFor`, as in `limits` of [REQ:plan-record](#req-plan-record). |

It has three outcomes: write a record; leave the record as it is; refuse, with a reason, and leave the record as it is. A refusal is also reported to the operator, which this contract does not describe. The outcomes are:

| At the provider | Outcome | `status` | `plan` | AI |
|---|---|---|---|---|
| `active`, last invoice paid | write | `active` | the paid tier | the plan's allowance |
| `active` with a cancellation scheduled | write | `active`, `endsAt` set | the paid tier | the plan's allowance until `endsAt` |
| `trialing` | write | `trialing` | the paid tier | the plan's allowance |
| `past_due` | write | `past_due`, `paidUntil` unchanged | the paid tier | the plan's allowance until the grace of [REQ:stale-rule](#req-stale-rule) ends, then Free |
| `unpaid` | write | `ended`, `endedReason: unpaid` | `free` | Free |
| `canceled`, last paid invoice not refunded in full | write | `ended`, `endedReason: canceled` | `free` | Free |
| `canceled`, last paid invoice refunded in full | write | `ended`, `endedReason: refunded` | `free` | Free |
| `paused` | write | `ended`, `endedReason: paused` | `free` | Free |
| `incomplete`, `incomplete_expired` | leave | | | no change (never paid) |
| `active`, `trialing` or `past_due` and the last paid invoice refunded in full | leave | | | no change: a refund alone ends nothing |
| `tier` does not fit `accountKind` (a tier for organisations on a personal account, or the reverse) | refuse, reason `tier_not_for_account` | | | no change |
| `tier` empty | refuse, reason `unknown_tier` | | | no change |

A refund is a reason an ended record carries, never a state of its own: a plan ends only when the subscription ends. The refund and the cancellation may arrive in either order; the function sees the same facts either way.

A written record, in every row: `v` is `1`; `plan`, `status`, `period`, `paidUntil` (when the fact is present), `founding` and `limits` are as in the table; `limits` is the `grants` fact. An ended record has `plan: free`, `period: none`, `founding: false` and no `limits`, and carries `endedReason` and the `paidUntil` of the facts. The function does not set `updatedAt` or `aiExtraQuestions`: the writer stamps the first and keeps the second.

Outside the function, in the same table, a dispute opened against a payment changes no record: the operator is told and decides. A second subscription paid for an account that already has one changes no record either.

Fixture: `state-table.json`: an object with a `cases` array; each case has `name`, `facts`, `outcome` (`write`, `leave` or `refuse`), and for `write` the written `record` (without `updatedAt` and `aiExtraQuestions`), for `refuse` the `reason`. One test per case.

### The endpoint a client reads

#### REQ: plan-endpoint-request

`GET /v0/datatug/plan` answers for the account that would pay for the caller's next question, in the context the caller gives. It is called with a Firebase ID token, or with a DataTug CLI sign-in that holds the scope `datatug:projects:read`, as `Authorization: Bearer <token>`. Without a credential it answers 401.

Optional query parameters: `account=<accountId>` and `project=<cloud project ID>`. They are hints and are treated exactly as the headers of [REQ:ai-headers](#req-ai-headers).

An `account` the caller is not a member of answers 403, whatever the reason (it does not exist, or the caller is not a member): the body is `{"error": {"code": "not_a_member", "message": "..."}}`. The same answer, for the same reason, hides whether an account exists.

Fixture: `plan-error-not-a-member.json`.

#### REQ: plan-endpoint-response

The answer is 200 and one JSON object of version 1. Every field below is always present unless it says otherwise.

| Field | Type | Meaning |
|---|---|---|
| `v` | number | `1`. |
| `payer` | string | `personal` or `organisation`: the kind of the account that would pay. |
| `accountId` | string | The account that would pay. |
| `accountTitle` | string | Its title: the person's name, or the organisation's. |
| `role` | string | The caller's role in that account (`owner`, `admin`, `creator`, `contributor`, `member`, or another a client does not know). |
| `plan` | string | The plan the record states (`free` when there is no record). |
| `effectivePlan` | string | The plan the server applies now. It differs from `plan` only under [REQ:stale-rule](#req-stale-rule). |
| `status` | string | As in the record; `none` when there is no record. |
| `period` | string | As in the record; `none` when there is no record. |
| `paidUntil` | timestamp, optional | As in the record, when there is one. |
| `founding` | boolean | |
| `limits` | object | Always present, unlike the record. It holds the effective limits: the Free ones for a Free or stale account. Same fields as `limits` of [REQ:plan-record](#req-plan-record). |
| `ai` | object | The AI allowance. See below. |
| `ownKeyWorks` | boolean | `true`: the person's own key works whatever else is true. A client says so at every limit. |
| `canUpgrade` | boolean | `false` for a member of an organisation who is not its owner or admin: the client then says to ask an admin of `accountTitle`. |
| `upgradeUrl` | string | Where to get a plan. A host setting. |
| `manageUrl` | string, optional | Where to manage the subscription (the payment provider's portal sign-in link). Present only for the owner or an admin of an account with a subscription. A host setting. |
| `supportEmail` | string, optional | A host setting. |
| `accounts` | array | The accounts the caller can choose between, at most twenty: the caller's personal account, and every organisation account that has a DataTug plan record and of which the caller is a proven member. Each is `{accountId, kind, title, plan, role, pays}`, `kind` being `personal` or `organisation` and `pays` marking those that could pay for the caller's questions under [REQ:payer-rules](#req-payer-rules). A space of another kind never appears. |

`ai`:

| Field | Type | Meaning |
|---|---|---|
| `unit` | string | `questions`. |
| `enforced` | boolean | `false` while the server only observes: the numbers are then informative and nothing is refused by them. |
| `periodId` | string | The UTC month the numbers are for, `YYYY-MM`. |
| `used`, `limit`, `left` | number | `left` is what [REQ:usage-record](#req-usage-record) computes, from the effective limit. |
| `resetsAt` | timestamp | The first instant of the next UTC month. |
| `today` | object | `{used, limit}`: the caller's own questions today (UTC) against the per-person daily ceiling, whichever account pays. The ceiling is set by configuration. |
| `blocked` | string or null | `null`, or the reason the next question would be refused although `left` is above 0: `daily`, `free_budget`, `unverified`, or `monthly` when the account is capped. A client treats a value it does not know as "the next question will be refused". |
| `models` | array | The models a client may request for this payer: `{id, class, weight, default}`. Exactly one has `default: true`; a client that names no model gets it. |

The `id` of a model is an opaque string. See [REQ:models](#req-models).

A client shows the plan, the account that pays, and `ai.left` of `ai.limit` with the reset date, and, while `ai.blocked` is not null, the reason. It does not compute any of those.

Fixtures: `plan-response-free.json`, `plan-response-pro.json`, `plan-response-team-member.json`, `plan-response-capped.json`, `plan-response-unverified.json`, `plan-response-ended.json`, `plan-response-stale.json`, `plan-response-observing.json`.

#### REQ: models

A response lists the models a payer may use, each with a `class` and a `weight`. A question counts its model's weight against the allowance. The contract carries more than one model and any weight so that models can be added without a change clients must follow; a response currently lists one model with weight 1, and a client must therefore tolerate several entries and any positive weight. A client never computes cost or `left` from weights: it shows `left` from the server. A request for a model that is not in `models` is refused with reason `model_class`.

### Which account pays

#### REQ: ai-headers

A client may send two hints on every `/v0/ai/*` call, and on `GET /v0/datatug/plan` as query parameters of the same meaning:

- `X-AI-Project: <cloud project ID>`: the project the person is working in.
- `X-AI-Account: <accountId>`: the account the person has chosen, or has in view.

A hint only selects among accounts the server has itself verified. It never gives an account a plan, a role or a limit. A hint the caller has no right to is ignored, not answered with an error that reveals whether the account or project exists. The product header `X-AI-Product: datatug` selects DataTug's allowance.

#### REQ: payer-rules

The server decides in this order:

1. **A project hint is present.** The server reads the project's account from its own records and checks the caller is in the project. If either fails, the hint is ignored (rule 4).
2. **That account is an organisation with a paying plan, and the caller is a contributor of it:** the organisation's pool pays.
3. **Anything else about that project** (it belongs to a personal account, the caller's or another person's; or the caller is only a guest or viewer of the organisation): the caller's own personal account pays. A person invited into another person's project is counted against their own allowance, never the owner's.
4. **No usable project hint.** The candidates are the caller's personal account if it is on a paying plan, and every organisation with a paying plan of which the caller is a proven contributor. One candidate: it pays. None: the personal account pays, on Free. Several: the one named by `X-AI-Account` if it is a candidate; otherwise the personal account.

Roles that draw on an organisation's pool: `owner`, `admin`, `creator`, `contributor`, `member`. A viewer-like role, and anyone outside the space's `userIDs`, do not.

Security properties a client may rely on: a plan, a limit or a role is never read from a request; an account ID or a project ID from a request is used only after a membership check made from the space's own records; a person can never make another person's personal account pay; a member of an organisation can make only that organisation's pool pay, and their use is recorded under their ID in `byMember`; the person's own daily ceiling applies whichever account pays.

#### REQ: account-header-needed

A project may name the account it belongs to; where it names none, rule 4 applies, and a person with more than one candidate (their own paying plan and an organisation, or two organisations) draws on an organisation's pool only if the client names it. So a client sends `X-AI-Account: <the account in view>` on every `/v0/ai/*` call, and a command-line client sends the account the person chose. `GET /v0/datatug/plan` lists the candidates in `accounts`, with `pays`, so that a client can ask the person once and remember the answer.

### What a question is

#### REQ: question

One question is one request of a person: all the model calls that serve one message they sent, a decision call included.

- The **question key** is the interaction ID of the request when the client sends a valid one (a UUID). A client sends the same ID on the decision call, on every chat call, and on a retry of the same message. Without a valid ID the server derives the key from a digest of the user ID, the product and the question text (the last user message of a chat request, the text of a decision request); only the digest is kept, never the text.
- The question is counted once, when its first model call is admitted. One key covers a bounded number of calls, within a bounded time of the first (both set by configuration); the next call under the same key starts, and counts, a new question.
- **Not counted:** a request refused before any model call; a question whose only admitted call fails on our side or the provider's before any output (it is given back); anything answered with the person's own key.
- **Counted:** a question the person aborts, cancels or disconnects from, at any moment; a question that fails mid-answer.

**Two accepted cases for a client that sends no ID**, which are counted as written and not as errors: (1) when a first pass is empty and the client retries with a different user message, the retry is a second question; (2) the same text sent twice within the time above ("yes", "continue") is one key, so it is counted once until the call limit of the key is reached. A client that sends an ID with every question, and the same one on every call of it, has neither.

#### REQ: allowance-on-the-wire

The last event of a chat stream and `GET /v0/ai/usage` carry the allowance as `{unit, used, limit, resetsAt}`. When `unit` is `questions`, a client shows `limit - used` as what is left after each answer. While `ai.enforced` is `false` the unit may be another one; a client shows the unit it is given, and never compares a `used` with a `limit` of another unit.

### What a person is told when a question is refused

#### REQ: refusal-body

A refusal is an HTTP error with a JSON body:

```json
{
  "error": {"code": "quota", "message": "A full sentence for a person."},
  "limit": {"v": 1, "reason": "monthly", "...": "..."}
}
```

`error.code` and `error.message` are the same as a client already handles; `error.message` is always a complete sentence that says what happened, when it resets, that the person's own key works, and where to get a plan. A client that knows nothing of `limit` shows `error.message`. A client that knows `limit` may build its own sentence from the fields, and then still names the own-key option. A client must never parse `error.message`.

`limit`, present for the reasons of the table and absent for `rate_limited` and `upstream`:

| Field | Type | Meaning |
|---|---|---|
| `v` | number | `1`. |
| `reason` | string | One of the reasons below. |
| `capped` | boolean | The money guard has stopped this account's included AI for the month. |
| `unit` | string | `questions`. |
| `payer` | string | `personal` or `organisation`. |
| `accountId`, `accountTitle` | string | The account that was to pay. |
| `plan` | string | Its plan in force. |
| `used`, `limit`, `left` | number | The monthly allowance of that account, whatever the reason. |
| `resetsAt` | timestamp, optional | When the refusal stops applying: the next UTC day for `daily`, the next UTC month for `monthly` and `free_budget`. Absent for `unverified`, `model_class` and `too_large`, which a reset does not end. |
| `ownKeyWorks` | boolean | `true`. |
| `canUpgrade` | boolean | As in [REQ:plan-endpoint-response](#req-plan-endpoint-response): `false` for a member of an organisation who is not its owner or admin. |
| `upgradeUrl` | string | As there. |

| `limit.reason` | HTTP status | `error.code` | Meaning |
|---|---|---|---|
| `monthly` | 429 | `quota` | The count is used up, or `capped` is true. |
| `daily` | 429 | `quota` | The person's daily ceiling. |
| `free_budget` | 429 | `quota` | All free AI is used up for the month, for everyone. |
| `unverified` | 403 | `invalid` | A Free allowance needs a trusted sign-in: a federated sign-in, or a verified email. Nothing was counted. |
| `model_class` | 403 | `invalid` | The plan does not include the model that was asked for. |
| `too_large` | 413 | `invalid` | The question sends more than included AI accepts for one call: narrow the tables, or use the own key. |
| none: `rate_limited` | 429 | `rate_limited` | A race for the counter was lost, or requests came too fast. A `Retry-After` header gives the seconds to wait. Nothing was counted. |
| none: `upstream` | 503 | `upstream` | The count or the plan cannot be read. Nothing was sent to a model, nothing was counted. |

At every refusal nothing was sent to a model and nothing was counted, except where a question was already running. Everything that is not AI continues to work.

Fixtures: `refusal-quota-monthly-free.json`, `refusal-quota-monthly-member.json`, `refusal-quota-capped.json`, `refusal-quota-daily.json`, `refusal-quota-free-budget.json`, `refusal-unverified.json`, `refusal-model-class.json`, `refusal-too-large.json`, `refusal-rate-limited.json`, `refusal-upstream.json`. The sentences in them show the kind of text the server sends; their wording is the server's and is not part of the contract.

### The public count of Founding places

#### REQ: offer-endpoint

`GET /v0/checkout/offer?site=datatug&offer=datatug-founding&mode=live` answers 200 with:

```json
{"offer": "datatug-founding", "percentOff": 30, "places": 6, "left": 4, "open": true}
```

- `percentOff`, `places` and `left` are numbers set by the offer; the ones above are made up.
- `left = places - held places`, never below 0. A held place is one that was paid for. An open payment form never lowers `left`, so a page can say "0 left" only when every place has been paid for.
- `open` is `false` when no place is left or the offer is not running.
- It needs no sign-in and reveals nothing about any customer. It answers with `Cache-Control: public` and a maximum age of one minute: the page shows a number, and the checkout decides. A person who presses Subscribe when the last place has just gone is told so before the payment form ([REQ:session-answer](#req-session-answer)).
- A page must send `mode=live`: with no `mode` the server answers for test mode, and test and live each have their own count.

Fixtures: `offer.json`, `offer-none-left.json`.

### Starting a purchase

A plan is bought after signing in, so a payment always lands on an account, and the plan appears there within seconds, with nobody doing anything else.

#### REQ: plan-prices

A pricing page takes the plans and their prices from `GET /v0/checkout/config?site=datatug&mode=live`: `publishableKey` (needed to draw the payment form), `site`, `mode`, and `plans`, one entry per plan with its `id` (the plan ID of [REQ:session-request](#req-session-request)), `label`, `interval`, and its price in minor units (`amount` in `currency`; `amounts` by currency). A page never holds a price of its own.

#### REQ: session-request

`POST /v0/checkout/session`, `Content-Type: application/json`, `Authorization: Bearer <Firebase ID token>`, body:

| Field | Type | Meaning |
|---|---|---|
| `site` | string | `datatug`. |
| `plan` | string | The plan ID: `datatug-<tier>-monthly` or `datatug-<tier>-annual`, `<tier>` being `pro`, `team`, `business` or `company`. |
| `mode` | string | `live` or `test`. Absent means test. |
| `spaceId` | string, optional | An existing organisation account to buy the plan for. |
| `accountTitle` | string, optional | The title of the organisation to create, when `spaceId` is absent. Default: the buyer's display name followed by "'s team". |

The server, not the client, chooses the account:

- `pro`: the buyer's personal account.
- `team`, `business`, `company` with a `spaceId`: that space, if the buyer is its owner or admin and it is an organisation account.
- `team`, `business`, `company` with no `spaceId`: no account is created by this request. The organisation is created, once, when the payment completes, with the buyer as owner and `accountTitle` as title.

An organisation plan for a person who already has Pro is a new purchase for a second account, never a change of the Pro subscription.

Fixtures: `checkout-session-request-personal.json`, `checkout-session-request-organisation-new.json`, `checkout-session-request-organisation-existing.json`.

#### REQ: session-answer

The answer is 200 with `clientSecret`, `sessionId` and `mode` (the mode the session was made in), and these:

| Field | Type | Meaning |
|---|---|---|
| `accountId` | string, optional | The account the plan will land on. Absent for an organisation that is created when the payment completes. |
| `accountKind` | string | `personal` or `organisation`. |
| `offer` | object | `{id, applied, percentOff, left}`. `applied` is `false` when no Founding place is left; the session is then made at list price, and a client shows that before it draws the payment form. |
| `amount` | object | `{currency, list, due, taxIncluded}`: `currency` is an ISO 4217 code in lower case; `list` and `due` are integers in the currency's minor unit, `due` is what the form will charge for the first period, and `taxIncluded` says whether the amounts include tax. |

A client shows `amount` and `offer` from the answer, not from its own tables.

Fixtures: `checkout-session-answer-personal.json`, `checkout-session-answer-organisation-new.json`, `checkout-session-answer-list-price.json`.

#### REQ: session-errors

A checkout error is a JSON object `{"code": "...", "message": "..."}`, with the status below. A client shows `message`, or its own sentence for a code it knows.

| Status | `code` | When |
|---|---|---|
| 401 | `sign_in_required` | No credential. Nothing was created. The page keeps the person's choice, asks them to sign in, and asks again. |
| 503 | `not_available` | The server cannot check a buyer or an account. No provider was called. |
| 403 | `not_account_admin` | The buyer may not buy for that space. The answer is identical for a space that does not exist. |
| 400 | `plan_not_for_account` | The tier does not fit the kind of account (a tier for organisations for a personal account, or the reverse). |
| 409 | `already_subscribed` | The account already has a live subscription. When the buyer's earlier payment form turns out to have been paid, the body also carries that form's `sessionId`, so the page can show the thank-you. |
| 409 | `offer_busy` | The last Founding places are held by open payment forms. No form is made and nothing is charged; the person is told to try again in a few minutes. A `Retry-After` header gives the seconds. |

A buyer has at most one open payment form: a new request closes the earlier one, which then ends for its tab.

Fixtures: `checkout-error-sign-in-required.json`, `checkout-error-not-available.json`, `checkout-error-not-account-admin.json`, `checkout-error-plan-not-for-account.json`, `checkout-error-already-subscribed.json`, `checkout-error-offer-busy.json`.

#### REQ: after-payment

The payment form is drawn from `clientSecret` and the `publishableKey` of [REQ:plan-prices](#req-plan-prices). When it completes, `GET /v0/checkout/session-status?site=datatug&session_id=<sessionId>&mode=<mode>` answers `status: "complete"` (with `customerEmail`, `plan` and `mode`), and the page shows the thank-you. The plan record follows the provider's event a moment later, so the page then calls `GET /v0/datatug/plan` until `plan` is no longer `free` for the account (every few seconds, for a bounded time), and says the plan is on its way if it still is. A person who bought for a new organisation learns its `accountId` from `accounts` of that answer.

### The fixtures

#### REQ: fixtures

`testdata/contract/` holds one JSON fixture per shape of this page, each named in this page, and `CHECKSUMS`: one line per fixture, `<SHA-256 of the file as 64 lower-case hex digits>`, two spaces, the file name, sorted by name, ending with a line feed (`shasum -a 256 -c CHECKSUMS` reads it). The test of `contract4datatug/contract_test.go` fails when a fixture is not valid JSON, is not listed, is listed and missing, or does not match its digest, and when a fixture is not named in this page.

A repository that uses the fixtures copies the whole directory and the test file, byte for byte (line endings included), and runs the same test against its copy. A fixture is changed here first, with `CHECKSUMS`, and then copied.

| Group | Fixtures | What they show |
|---|---|---|
| Plan record | `plan-record-pro-active.json`, `plan-record-team-cancel-scheduled.json`, `plan-record-pro-past-due.json`, `plan-record-ended-refunded.json` | The documents of [REQ:plan-record](#req-plan-record). |
| Count | `ai-usage-record.json`, `ai-usage-record-capped.json` | The documents of [REQ:usage-record](#req-usage-record). |
| State table | `state-table.json` | The cases of [REQ:state-table](#req-state-table). |
| Plan endpoint | `plan-response-free.json`, `plan-response-pro.json`, `plan-response-team-member.json`, `plan-response-capped.json`, `plan-response-unverified.json`, `plan-response-ended.json`, `plan-response-stale.json`, `plan-response-observing.json`, `plan-error-not-a-member.json` | A Free person; a Pro owner; a member of an organisation; an account stopped for the month; a sign-in that is not trusted; an ended subscription; a record that no longer holds (`effectivePlan` differs); a server that only observes (`ai.enforced` false); the 403. |
| Refusals | `refusal-quota-monthly-free.json`, `refusal-quota-monthly-member.json`, `refusal-quota-capped.json`, `refusal-quota-daily.json`, `refusal-quota-free-budget.json`, `refusal-unverified.json`, `refusal-model-class.json`, `refusal-too-large.json`, `refusal-rate-limited.json`, `refusal-upstream.json` | One body per row of [REQ:refusal-body](#req-refusal-body). The status of each is in that table. |
| Offer | `offer.json`, `offer-none-left.json` | [REQ:offer-endpoint](#req-offer-endpoint). |
| Purchase | `checkout-session-request-personal.json`, `checkout-session-request-organisation-new.json`, `checkout-session-request-organisation-existing.json`, `checkout-session-answer-personal.json`, `checkout-session-answer-organisation-new.json`, `checkout-session-answer-list-price.json`, `checkout-error-sign-in-required.json`, `checkout-error-not-available.json`, `checkout-error-not-account-admin.json`, `checkout-error-plan-not-for-account.json`, `checkout-error-already-subscribed.json`, `checkout-error-offer-busy.json` | [REQ:session-request](#req-session-request), [REQ:session-answer](#req-session-answer), [REQ:session-errors](#req-session-errors). The status of each error is in that table. |

#### REQ: numbers-are-configuration

No price, allowance, count of places, ceiling or length of a grace is part of this contract. Each is set by configuration on the server, and a client takes it from a response. A number in a fixture is made up and says nothing about any plan; a client must not use one as a default.

## Acceptance Criteria

### AC: show-plan-and-left

Given the answer in `plan-response-pro.json`
When a client shows the plan and the allowance
Then it shows the plan `pro`, `ai.left` of `ai.limit` questions, the reset date from `ai.resetsAt`, and a link to `manageUrl`, and it computes none of them.

### AC: free-without-record

Given a person with no plan record, and the answer in `plan-response-free.json`
When a client shows the plan
Then it shows Free with the numbers of the response, and no link to manage a subscription.

### AC: organisation-member

Given the answer in `plan-response-team-member.json`
When a client shows it to a member of the organisation
Then it shows that the organisation's pool pays, shows `ai.left` of the pool, does not offer an upgrade (`canUpgrade` is `false`) and says to ask an admin of `accountTitle`, and sends `X-AI-Account` with that `accountId` on its AI calls.

### AC: blocked-with-questions-left

Given the answers in `plan-response-capped.json` and `plan-response-unverified.json`
When a client shows what is left
Then in both it shows that the next question will be refused, with the reason from `ai.blocked`, and in the second one `ai.left` is not 0 although the question will be refused.

### AC: stale-record

Given the answer in `plan-response-stale.json`, whose `plan` is `pro` and whose `effectivePlan` is `free`
When a client shows the plan
Then it shows the effective plan and the effective limits, not the plan of the record.

### AC: refusal-sentence

Given each body of `refusal-quota-monthly-free.json`, `refusal-quota-capped.json`, `refusal-quota-daily.json`, `refusal-quota-free-budget.json`, `refusal-unverified.json`, `refusal-model-class.json` and `refusal-too-large.json`
When a client receives it with the status of its row of [REQ:refusal-body](#req-refusal-body)
Then it tells the person what happened, when it resets (where `resetsAt` is present), that their own key works, and where to get a plan (only while `canUpgrade` is `true`); a client that does not know `limit` shows `error.message`.

### AC: refusal-without-limit

Given the bodies of `refusal-rate-limited.json` and `refusal-upstream.json`
When a client receives them
Then it shows `error.message`, treats nothing as counted, and offers the own key.

### AC: record-rows

Given each case of `state-table.json`
When the facts of the case are given to the function of [REQ:state-table](#req-state-table)
Then the outcome is the case's `outcome`, and for `write` the record equals the case's `record` apart from `updatedAt` and `aiExtraQuestions`.

### AC: record-shapes

Given each fixture whose name begins `plan-record-` or `ai-usage-record`
When it is read as the document of [REQ:plan-record](#req-plan-record) or [REQ:usage-record](#req-usage-record)
Then every field of the fixture is a field of that requirement and every required field is present.

### AC: founding-count

Given `offer.json` and `offer-none-left.json`
When a pricing page shows the number of places
Then it shows `left` of `places` for the first, and that none is left for the second, and it never counts open payment forms.

### AC: purchase-flow

Given a person who is not signed in and presses Subscribe on a plan
When the page posts the request of `checkout-session-request-personal.json` without a credential
Then it receives the 401 of `checkout-error-sign-in-required.json`, keeps the chosen plan, signs the person in, and posts the request again with the credential; the answer is the one of `checkout-session-answer-personal.json`, whose `amount` and `offer` the page shows before it draws the payment form.

### AC: no-place-left

Given the answer in `checkout-session-answer-list-price.json`
When the page is about to draw the payment form
Then it says before the form that the offer is taken and shows `amount.due`, which equals `amount.list`.

### AC: purchase-refusals

Given the six bodies of the `checkout-error-` fixtures
When a page receives each with its status from [REQ:session-errors](#req-session-errors)
Then it shows the person a sentence for it, offers a retry only for `offer_busy`, offers sign-in only for `sign_in_required`, and shows the thank-you for `already_subscribed` only when the body carries a `sessionId`.

### AC: fixtures-intact

Given a copy of `testdata/contract/` and of `contract_test.go` in a client's repository
When its tests run
Then they pass when every fixture is byte for byte the one here, and fail when a fixture is edited, added, removed or not listed in `CHECKSUMS`.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
