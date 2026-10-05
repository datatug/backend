---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Plans and AI metering

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/backend/spec/features/plans-and-ai-metering?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

The contract between the server side of DataTug plans and AI metering and every client that shows it: the records that hold a plan and a month's count, the endpoint that gives a client the plan and what is left, which account pays for a question, the models a payer may choose from and what each costs in questions, the refusal at a limit, the public count of Founding places and the request that starts a purchase. Fixtures in `testdata/contract/`, pinned by `CHECKSUMS`, give one example of each shape. No price, allowance, count of places or money ceiling is written here: a number is set by configuration and a client reads it from a response.

## Problem

A client (the web app, the CLI) has to show three things: which plan an account is on, how many AI questions are left this month, and, at a limit, why a question was refused and what the person can do. It must do so without asking the server team a question and without guessing a number. Without one written contract, each client reads the records its own way, computes `left` its own way, and parses a refusal sentence. A client that copies a number into its code is wrong the day the number changes.

This page fixes the shapes. The fixtures are copied into each client's repository together with `CHECKSUMS` and the test `contract4datatug/contract_test.go` of this repository, which is standard-library only and reads nothing but the fixtures; each copy is checked against the same digests, so a copy cannot drift unnoticed. The layout a copy needs is in [REQ:fixtures](#req-fixtures).

## Behavior

### Words used

- **Account**: the thing that holds a plan. An account is a Sneat space, and `accountId` is the ID of that space. A **personal account** is a person's personal space. An **organisation account** is a space of type `company`. Membership and roles are the space's own; this contract stores no owner or member list.
- **Plan**: one of `free`, `pro`, `team`, `business`, `company`, `enterprise`. A personal account is on `free` or `pro`. An organisation account is on `team`, `business` or `company`. `enterprise` is reserved; it is not sold through checkout. A client must show a plan it does not know by its name and must not fail on it.
- **Question**: what the allowance counts, defined in [REQ:question](#req-question).
- **Payer**: the account whose allowance a question is counted against.
- **Buyer**: the signed-in person who pays for a plan.
- **Place**: one of the limited places of the Founding offer ([REQ:offer-endpoint](#req-offer-endpoint)).
- **Model**: one of the AI models a payer may ask, listed in `ai.models` ([REQ:models](#req-models)). Each has an opaque `id`, a `class` and a `weight`.
- **Own key or own endpoint**: an AI key, or the address of an AI endpoint, that the person supplies. The client calls it itself, from the person's machine or browser. The key and the address are never sent to this server and never stored by it, so a question answered this way never reaches the server and is never counted or limited. Every refusal says so.
- **UTC**: every instant is an RFC 3339 timestamp in UTC, and every month and day boundary is a UTC one. In a stored document the writer uses the data store's own timestamp type; a reader accepts that or an RFC 3339 string.

Any field a client does not know is ignored, and any enum value a client does not know is treated as the safest reading named below: an unknown `plan` is shown by name, an unknown `status` is treated as `none` ([REQ:status-meaning](#req-status-meaning)), an unknown `blocked` or `reason` is treated as "no question can be asked now" and the response's own sentence is shown.

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
| `limits.aiModelClasses` | array of string | The model classes the plan includes: `fast` (in every plan) and `standard`. Others may be added, and a client tolerates an unknown class. A client offers only the models that `ai.models` lists ([REQ:models](#req-models)). |
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
| `used` | number | Questions used, each counted by the weight of the model it ran on ([REQ:models](#req-models)). It is above `questions` once a question ran on a model whose weight is above 1. |
| `questions` | number | Questions asked, unweighted. |
| `byMember` | map of user ID to number | Weighted use per member of an organisation, like `used`. Optional: a reader tolerates its absence. |
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
4. The endpoint carries neither `endsAt` nor `endedReason`. A client that wants to show a scheduled cancellation or why a plan ended may read those two fields from `plan/current` of the account that pays, and shows nothing about them when it cannot read the document.

#### REQ: stale-rule

A plan holds only while it is paid for. The server treats a record as Free when the current time is later than `paidUntil` plus a grace, whose length is set by configuration and is longer for `past_due` than for `active` and `trialing`. A paying `status` with no `paidUntil` is Free. The endpoint reports the result as `effectivePlan` ([REQ:plan-endpoint-response](#req-plan-endpoint-response)); a client shows `effectivePlan` and the effective `limits`, and never recomputes the grace.

Fixture: `plan-response-stale.json` (the record says `pro` and `past_due`; the plan in force is `free`).

#### REQ: fail-towards-free

A plan record that cannot be read, a version the server does not know, a status nobody mapped, or a missing number gives the person the Free allowance or a clear refusal, never an unlimited one: an unreadable plan record is not a refusal, the person gets Free. Where the count cannot be read or written, or a money counter cannot be read, or the caller's personal account cannot be found, the server answers 503 with `error.code` `upstream` and sends nothing to a model ([REQ:refusal-body](#req-refusal-body)).

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
| `refundedAt` | timestamp, optional | When. The function ignores it: it decides no row and no field of a record. |
| `firstPaidAt` | timestamp, optional | The time of the first payment. The function ignores it as well; absent when the subscription was never paid. |
| `founding` | boolean | The Founding discount is on the subscription. |
| `grants` | object | The limits the plan allows: `contributors`, `projectGuests`, `aiQuestions`, `aiModelClasses`, `aiPaysFor`, as in `limits` of [REQ:plan-record](#req-plan-record). |

It has three outcomes: write a record; leave the record as it is; refuse, with a reason, and leave the record as it is. A refusal is also reported to the operator, which this contract does not describe. `refundedAt` and `firstPaidAt` are passed so that a caller hands over what it read; no outcome depends on them. The outcomes are:

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

The rows can overlap (an ending status with a tier that does not fit; `incomplete` with an empty tier). The function applies them in this order, and the first that matches decides:

1. `providerStatus` is `unpaid`, `canceled` or `paused`: write the ended record, whatever the tier. The reason is `refunded` only for `canceled` with the last paid invoice refunded in full; `unpaid` and `paused` keep their own reason.
2. `providerStatus` is `incomplete` or `incomplete_expired`: leave.
3. `tier` is empty: refuse, `unknown_tier`.
4. `tier` does not fit `accountKind`: refuse, `tier_not_for_account`.
5. `providerStatus` is `active`, `trialing` or `past_due` and `lastInvoiceRefundedInFull` is true: leave.
6. Otherwise: write.

A refund is a reason an ended record carries, never a state of its own: a plan ends only when the subscription ends. The refund and the cancellation may arrive in either order; the function sees the same facts either way.

A written record, in every row: `v` is `1`; `plan`, `status`, `period`, `paidUntil` (when the fact is present), `founding` and `limits` are as in the table; `limits` is the `grants` fact. An ended record has `plan: free`, `period: none`, `founding: false` and no `limits`, and carries `endedReason` and the `paidUntil` of the facts. The function does not set `updatedAt` or `aiExtraQuestions`: the writer stamps the first and keeps the second.

Two things are neither facts nor rows of the function: a dispute opened against a payment changes no record (the operator is told and decides), and a second subscription paid for an account that already has one changes no record either. The caller handles both before it calls the function.

Fixture: `state-table.json`: an object with a `cases` array; each case has `name`, `facts`, `outcome` (`write`, `leave` or `refuse`), and for `write` the written `record` (without `updatedAt` and `aiExtraQuestions`), for `refuse` the `reason`. There is a case for each row and for each overlap above, and some `grants` carry two model classes and one carries one. One test per case.

### What a status means for the person

#### REQ: status-meaning

The `status` of the endpoint answer (and of the record) tells a client what to say. The plan in force is always `effectivePlan` and the limits are `limits`; a client never shows a plan the server does not apply.

| `status` | What the client shows | What the person can do |
|---|---|---|
| `none` | The plan in force, Free. | Choose a plan at `upgradeUrl`, where `canUpgrade` is `true`. |
| `active` | `effectivePlan`. When `plan/current` carries `endsAt` ([REQ:direct-readers](#req-direct-readers)), that the plan ends then. | Manage the subscription at `manageUrl` when it is present. |
| `trialing` | As `active`. | As `active`. |
| `past_due` | `effectivePlan`, and that the last payment failed. The plan still holds until the grace of [REQ:stale-rule](#req-stale-rule) ends; after it `effectivePlan` is `free` and the client shows that. | Fix the payment at `manageUrl` when it is present. |
| `ended` | Free. That the plan ended; when `plan/current` carries `endedReason`, why. | Choose a plan at `upgradeUrl`. |
| any other value | `effectivePlan`, and nothing about payment. | As `none`. |

Fixtures: `plan-response-ended.json` (`ended`), `plan-response-stale.json` (`past_due`, no longer in force), `plan-response-pro.json` (`active`).

### The endpoint a client reads

#### REQ: plan-endpoint-request

`GET /v0/datatug/plan`, on the API base the client is configured with ([REQ:ai-calls](#req-ai-calls)), answers for the account that would pay for the caller's next question, in the context the caller gives. It is called with a Firebase ID token, or with a DataTug CLI sign-in that holds the scope `datatug:projects:read`, as `Authorization: Bearer <token>`. A missing, expired or wrongly scoped credential answers 401. The contract gives a 401 no body: a client reads any 401, here and on every `/v0/ai/*` call, as "sign in again" and depends on nothing in its body.

Optional query parameters:

- `project=<cloud project ID>` has the meaning of `X-AI-Project` ([REQ:ai-headers](#req-ai-headers)): one the caller has no right to is ignored, and never answered with an error.
- `account=<accountId>` is checked. An `account` the caller is not a member of answers 403, whatever the reason (it does not exist, or the caller is not a member): the body is `{"error": {"code": "not_a_member", "message": "..."}}`. The same answer, for the same reason, hides whether an account exists.

A client passes `account` only with an `accountId` taken from `accounts` of an earlier answer. In a project of an account it is not a member of, it passes `project` or nothing. The answer is always for the account that would pay under [REQ:payer-rules](#req-payer-rules), so its `accountId` may differ from the `account` asked for: the client shows the account the answer names.

When the server cannot read the count, the answer is 503 with the `upstream` body of [REQ:refusal-body](#req-refusal-body); a client then shows no number and says so.

A client that meets a `v` other than 1 in the answer shows no number, says that the app is out of date and that the person's own key still works, and calls nothing else of this contract.

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
| `accounts` | array | The accounts the caller can choose between, at most twenty: the caller's personal account, and every organisation account that has a DataTug plan record and of which the caller is a proven member. Each is `{accountId, kind, title, plan, role, pays}`, `kind` being `personal` or `organisation`; `plan` is the plan in force of that account (its `effectivePlan`, not the plan its record states); `pays` marks the candidates of rule 4 of [REQ:payer-rules](#req-payer-rules), the accounts that could pay for the caller's questions when no usable project hint is given, so a client can ask the person which one. A space of another kind never appears. |

`ai`:

| Field | Type | Meaning |
|---|---|---|
| `unit` | string | `questions`. |
| `enforced` | boolean | `false` while the server only observes: the numbers are then informative and nothing is refused by them. |
| `periodId` | string | The UTC month the numbers are for, `YYYY-MM`. |
| `used`, `limit`, `left` | number | `used` is the weighted use of the month ([REQ:usage-record](#req-usage-record)). `limit` is the effective `limits.aiQuestions` plus `aiExtraQuestions`. `left` is `max(0, limit - used)`, or 0 when the account is capped. |
| `resetsAt` | timestamp | The first instant of the next UTC month. |
| `today` | object | `{used, limit}`: the caller's own questions today (UTC) against the per-person daily ceiling, whichever account pays. The ceiling is set by configuration. |
| `blocked` | string or null | The reason the next question would be refused by something other than the count, or `null`. The reasons are `daily`, `free_budget`, `unverified`, and `monthly` when the account is capped. `blocked` is `null` when nothing but the count could stop the next question, and so also when the month's count is simply used up (`left` is 0 and the account is not capped): `left` shows that. A client treats a value it does not know as "the next question will be refused". |
| `models` | array | The models a client may request for this payer: `{id, class, weight, default}`. Exactly one has `default: true`; a client that names no model gets it. See [REQ:models](#req-models). |

A person can ask a model of weight `w` now when `ai.blocked` is `null` and `ai.left` is at least `w`. A client shows the plan, the account that pays, and `ai.left` of `ai.limit` with the reset date, and, while `ai.blocked` is not null, the reason. It does not compute any of those.

Fixtures: `plan-response-free.json`, `plan-response-pro.json`, `plan-response-pro-one-left.json`, `plan-response-team-member.json`, `plan-response-several-accounts.json`, `plan-response-capped.json`, `plan-response-used-up.json`, `plan-response-unverified.json`, `plan-response-ended.json`, `plan-response-stale.json`, `plan-response-observing.json`.

#### REQ: models

The models a payer may use are a list, `ai.models`, set by configuration and different for different payers. It may hold several models. Each has a `class` and a `weight` (a positive number), and `ai.models` holds only models whose class is in `limits.aiModelClasses`. The rules:

- **A client chooses from the list.** It offers the models of `ai.models`, preselects the one with `default: true`, and may show each weight (a model of weight 3 uses three questions of the allowance). It never offers a model that is not listed, and never computes cost or `left` from weights: it shows `left` from the server.
- **A request names its model in the field `model`** of the chat request ([REQ:ai-calls](#req-ai-calls)): an `id` of the list. A request that names none, or names `auto`, runs on the default. An `id` is an opaque string a client never interprets.
- **A question counts the weight of the model it ran on** against the allowance, in `used`; `questions` counts it once ([REQ:usage-record](#req-usage-record)).
- **A model that is not in the list is refused** with reason `model_class` (403), whether its class is not included in the plan or the server does not know it. Nothing is counted.
- **A model whose weight is above `ai.left` is refused** with reason `monthly` (429) and nothing is counted; a model of lower weight is still admitted. The refusal carries `left`, so a client can offer a lighter model.
- **Own key or own endpoint.** A person's own key or endpoint is no model of this list. The client calls it itself, from the person's machine or browser; it never sends the key or the address to this server, and a question answered this way is never counted or limited.

Fixtures: `plan-response-free.json` (one model), `plan-response-pro.json` (three models in two classes, two weights, one default), `plan-response-pro-one-left.json` (`left` below the weight of one model), `refusal-model-class.json`, `refusal-quota-monthly-weight.json`.

### The AI calls

#### REQ: ai-calls

The AI calls of a client are the public protocol of the package `ai/cloudproto` of `github.com/strongo/aichat`, on the API base the client is configured with (its origin, and the `/v0/` prefix; `GET /v0/datatug/plan` and `/v0/checkout/...` are on the same origin):

- `POST /v0/ai/chat` (a stream of events), `POST /v0/ai/decision`, `GET /v0/ai/usage`.
- A client sends `X-AI-Product: datatug` on every `/v0/ai/*` call; without it the DataTug allowance does not apply.
- The model is the field `model` of the chat request body; `interactionId` is the field of the chat, decision and score request bodies that carries the question key ([REQ:question](#req-question)).
- The allowance after an answer is in `usage.allowance` of the last event of a chat stream (`response.completed`) and in `allowance` of `GET /v0/ai/usage` ([REQ:allowance-on-the-wire](#req-allowance-on-the-wire)).
- An error is an HTTP error with the body `{"error": {"code", "message"}}`, plus `limit` at a refusal ([REQ:refusal-body](#req-refusal-body)).

### Which account pays

#### REQ: ai-headers

A client may send two hints on every `/v0/ai/*` call:

- `X-AI-Project: <cloud project ID>`: the project the person is working in.
- `X-AI-Account: <accountId>`: the account the person has chosen, or has in view.

On `GET /v0/datatug/plan` the same two are query parameters, `project` and `account`, with one difference stated in [REQ:plan-endpoint-request](#req-plan-endpoint-request): there an `account` the caller is not a member of answers 403.

A hint only selects among accounts the server has itself verified. It never gives an account a plan, a role or a limit. On `/v0/ai/*` a hint the caller has no right to is ignored, not answered with an error that reveals whether the account or project exists. The product header `X-AI-Product: datatug` is required ([REQ:ai-calls](#req-ai-calls)).

#### REQ: payer-rules

The server decides in this order:

1. **A project hint is present.** The server reads the project's account from its own records and checks the caller is in the project. If either fails, the hint is ignored (rule 4).
2. **That account is an organisation with a paying plan, and the caller is a contributor of it:** the organisation's pool pays.
3. **Anything else about that project** (it belongs to a personal account, the caller's or another person's; or the caller is only a guest or viewer of the organisation): the caller's own personal account pays. A person invited into another person's project is counted against their own allowance, never the owner's.
4. **No usable project hint.** The candidates are the caller's personal account if it is on a paying plan, and every organisation with a paying plan of which the caller is a proven contributor. One candidate: it pays. None: the personal account pays, on Free. Several: the one named by `X-AI-Account` if it is a candidate; otherwise the personal account.

Roles that draw on an organisation's pool: `owner`, `admin`, `creator`, `contributor`, `member`. A viewer-like role, and anyone outside the space's `userIDs`, do not.

Security properties a client may rely on: a plan, a limit or a role is never read from a request; an account ID or a project ID from a request is used only after a membership check made from the space's own records; a person can never make another person's personal account pay; a member of an organisation can make only that organisation's pool pay, and their use is recorded under their ID in `byMember`; the person's own daily ceiling applies whichever account pays.

#### REQ: account-header-needed

A project may name the account it belongs to; where it names none, rule 4 applies, and a person with more than one candidate (their own paying plan and an organisation, or two organisations) draws on an organisation's pool only if the client names it. So a client sends `X-AI-Account: <the account in view>` on every `/v0/ai/*` call, when the account in view is one of `accounts` (a project of an account the person is not a member of has no header: the person's own account pays there), and a command-line client sends the account the person chose. `GET /v0/datatug/plan` lists the candidates in `accounts`, with `pays`, so that a client can ask the person once and remember the answer.

### What a question is

#### REQ: question

One question is one request of a person: all the model calls that serve one message they sent, a decision call included.

- The **question key** is the interaction ID of the request (the `interactionId` field, [REQ:ai-calls](#req-ai-calls)) when the client sends a valid one (a UUID). A client sends the same ID on the decision call, on every chat call, and on a retry of the same message. Without a valid ID the server derives the key from a digest of the user ID, the product and the question text (the last user message of a chat request, the text of a decision request); only the digest is kept, never the text.
- The question is counted once, when its first model call is admitted. One key covers a bounded number of calls, within a bounded time of the first (both set by configuration); the next call under the same key starts, and counts, a new question.
- **Not counted:** a request refused before any model call; a question whose only admitted call fails on our side or the provider's before any output (it is given back); anything answered with the person's own key.
- **Counted:** a question the person aborts, cancels or disconnects from, at any moment; a question that fails mid-answer.

**Two accepted cases for a client that sends no ID**, which are counted as written and not as errors: (1) when a first pass is empty and the client retries with a different user message, the retry is a second question; (2) the same text sent twice within the time above ("yes", "continue") is one key, so it is counted once until the call limit of the key is reached. A client that sends an ID with every question, and the same one on every call of it, has neither.

#### REQ: allowance-on-the-wire

The last event of a chat stream (in `usage.allowance`) and `GET /v0/ai/usage` (in `allowance`) carry the allowance as `{unit, used, limit, resetsAt}`. When `unit` is `questions`, a client shows `limit - used` as what is left after each answer. While `ai.enforced` is `false` the unit may be another one; a client shows the unit it is given, and never compares a `used` with a `limit` of another unit.

### What a person is told when a question is refused

#### REQ: refusal-body

A refusal is an HTTP error with a JSON body:

```json
{
  "error": {"code": "quota", "message": "A full sentence for a person."},
  "limit": {"v": 1, "reason": "monthly", "...": "..."}
}
```

`error.code` and `error.message` are the same as a client already handles; `error.message` is always a complete sentence that says what happened, what the person can do, and that the person's own key works; where a reset or a plan would help, it says when and where. A client that knows nothing of `limit` shows `error.message`. A client that meets a `limit.v` other than 1 shows `error.message` only. A client that knows `limit` may build its own sentence from the fields, and then still names the own-key option. A client must never parse `error.message`.

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
| `monthly` | 429 | `quota` | The count is used up, or `capped` is true, or `left` is below the weight of the model asked for ([REQ:models](#req-models)). |
| `daily` | 429 | `quota` | The person's daily ceiling. |
| `free_budget` | 429 | `quota` | All free AI is used up for the month, for everyone. |
| `unverified` | 403 | `invalid` | A Free allowance needs a trusted sign-in: a federated sign-in, or a verified email. Nothing was counted. |
| `model_class` | 403 | `invalid` | The model that was asked for is not in the payer's `ai.models`: its class is not included in the plan, or it is not known. |
| `too_large` | 413 | `invalid` | The question sends more than included AI accepts for one call: narrow the tables, or use the own key. |
| none: `rate_limited` | 429 | `rate_limited` | A race for the counter was lost, or requests came too fast. A `Retry-After` header gives the seconds to wait. Nothing was counted. |
| none: `upstream` | 503 | `upstream` | The count cannot be read or written, or a money counter cannot be read, or the caller's personal account cannot be found. Nothing was sent to a model, nothing was counted. (A plan record that cannot be read is no refusal: the person gets Free, [REQ:fail-towards-free](#req-fail-towards-free).) |

At every refusal nothing was sent to a model and nothing was counted, except where a question was already running. Everything that is not AI continues to work.

Fixtures: `refusal-quota-monthly-free.json`, `refusal-quota-monthly-member.json`, `refusal-quota-monthly-weight.json`, `refusal-quota-capped.json`, `refusal-quota-daily.json`, `refusal-quota-free-budget.json`, `refusal-unverified.json`, `refusal-model-class.json`, `refusal-too-large.json`, `refusal-rate-limited.json`, `refusal-upstream.json`. The `message` in each is a placeholder: the wording is the server's and is not part of the contract.

### The public count of Founding places

#### REQ: offer-endpoint

`GET /v0/checkout/offer?site=datatug&offer=datatug-founding&mode=live` answers 200 with:

```json
{"offer": "datatug-founding", "percentOff": 30, "places": 6, "left": 4, "open": true}
```

- `percentOff`, `places` and `left` are numbers set by the offer; the ones above are made up.
- `left = places - held places`, never below 0. A held place is one that was paid for. An open payment form never lowers `left`, so a page can say "0 left" only when every place has been paid for.
- `open` is `false` when no place is left or the offer is not running.
- It needs no sign-in and reveals nothing about any customer. A browser may call it from the storefront's own origins. It answers with `Cache-Control: public` and a maximum age of one minute: the page shows a number, and the checkout decides. A person who presses Subscribe when the last place has just gone is told so before the payment form ([REQ:session-answer](#req-session-answer)).
- A page sends `mode=live`. Test and live each have their own count.

Fixtures: `offer.json`, `offer-none-left.json`.

### Starting a purchase

A plan is bought after signing in, so a payment always lands on an account, and the plan appears there within seconds, with nobody doing anything else.

#### REQ: session-request

`POST /v0/checkout/session`, `Content-Type: application/json`, `Authorization: Bearer <Firebase ID token>`, body:

| Field | Type | Meaning |
|---|---|---|
| `site` | string | `datatug`. |
| `plan` | string | The plan ID: `datatug-<tier>-monthly` or `datatug-<tier>-annual`, `<tier>` being `pro`, `team`, `business` or `company`. |
| `mode` | string | `live` or `test`. A page sends `live`. |
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
| `offer` | object, optional | `{id, applied, percentOff, left}`. Absent when no offer applies to the plan: `amount.due` then equals `amount.list`. Present with `applied: false` when an offer applies to the plan but no Founding place is left; the session is then made at list price, and a client shows that before it draws the payment form. |
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

Fixtures: `checkout-error-sign-in-required.json`, `checkout-error-not-available.json`, `checkout-error-not-account-admin.json`, `checkout-error-plan-not-for-account.json`, `checkout-error-already-subscribed.json`, `checkout-error-already-subscribed-no-session.json`, `checkout-error-offer-busy.json`.

#### REQ: after-payment

The payment form is drawn from `clientSecret`; the checkout's own documentation covers drawing the form and the thank-you. The plan record follows the provider's event a moment later, so after the form completes the page calls `GET /v0/datatug/plan` every few seconds, for a bounded time, until the entry of `accounts` for the account bought for carries the bought plan (for the personal account or an existing organisation, the entry with that `accountId`; for a new organisation, until an organisation entry that was not there before appears), and says the plan is on its way if it still does not. A purchase made in test mode never changes the endpoint ([REQ:test-record](#req-test-record)). A person who bought for a new organisation learns its `accountId` from `accounts` of that answer.

### The fixtures

#### REQ: fixtures

`testdata/contract/` holds one JSON fixture per shape of this page, each named in this page, and `CHECKSUMS`: one line per fixture, `<SHA-256 of the file as 64 lower-case hex digits>`, two spaces, the file name, sorted by name, ending with a line feed (`shasum -a 256 -c CHECKSUMS` reads it). The test of `contract4datatug/contract_test.go` fails when a fixture is not valid JSON, is not listed, is listed and missing, or does not match its digest. It reads nothing but the fixtures. A second test, `contract4datatug/page_test.go`, fails when a fixture is not named in this page; it checks this repository's own page and is not copied.

A repository that uses the fixtures copies the whole directory and `contract_test.go`, byte for byte, and runs the same test against its copy. The layout a copy needs:

- `testdata/contract/` at the repository root, with `CHECKSUMS` inside it;
- `contract_test.go` alone in a directory one level below the root (any name; the file is an external test package, so no other file is needed);
- the line `testdata/contract/** -text` in the repository's `.gitattributes`, so that a checkout never changes a line ending and a digest holds on every machine.

A fixture is changed here first, with `CHECKSUMS`, and then copied.

| Group | Fixtures | What they show |
|---|---|---|
| Plan record | `plan-record-pro-active.json`, `plan-record-team-cancel-scheduled.json`, `plan-record-pro-past-due.json`, `plan-record-ended-refunded.json` | The documents of [REQ:plan-record](#req-plan-record). |
| Count | `ai-usage-record.json`, `ai-usage-record-capped.json` | The documents of [REQ:usage-record](#req-usage-record); the first has `used` above `questions`. |
| State table | `state-table.json` | The cases of [REQ:state-table](#req-state-table). |
| Plan endpoint | `plan-response-free.json`, `plan-response-pro.json`, `plan-response-pro-one-left.json`, `plan-response-team-member.json`, `plan-response-several-accounts.json`, `plan-response-capped.json`, `plan-response-used-up.json`, `plan-response-unverified.json`, `plan-response-ended.json`, `plan-response-stale.json`, `plan-response-observing.json`, `plan-error-not-a-member.json` | A Free person with one model; a Pro owner with three models in two classes; a Pro owner with one question left; a member of an organisation; a person with two accounts that could pay; an account stopped for the month; a Free month used up (`blocked` is `null`); a sign-in that is not trusted; an ended subscription; a record that no longer holds (`effectivePlan` differs); a server that only observes (`ai.enforced` false); the 403. |
| Refusals | `refusal-quota-monthly-free.json`, `refusal-quota-monthly-member.json`, `refusal-quota-monthly-weight.json`, `refusal-quota-capped.json`, `refusal-quota-daily.json`, `refusal-quota-free-budget.json`, `refusal-unverified.json`, `refusal-model-class.json`, `refusal-too-large.json`, `refusal-rate-limited.json`, `refusal-upstream.json` | One body per row of [REQ:refusal-body](#req-refusal-body). The status of each is in that table. |
| Offer | `offer.json`, `offer-none-left.json` | [REQ:offer-endpoint](#req-offer-endpoint). |
| Purchase | `checkout-session-request-personal.json`, `checkout-session-request-organisation-new.json`, `checkout-session-request-organisation-existing.json`, `checkout-session-answer-personal.json`, `checkout-session-answer-organisation-new.json`, `checkout-session-answer-list-price.json`, `checkout-error-sign-in-required.json`, `checkout-error-not-available.json`, `checkout-error-not-account-admin.json`, `checkout-error-plan-not-for-account.json`, `checkout-error-already-subscribed.json`, `checkout-error-already-subscribed-no-session.json`, `checkout-error-offer-busy.json` | [REQ:session-request](#req-session-request), [REQ:session-answer](#req-session-answer), [REQ:session-errors](#req-session-errors). The status of each error is in that table. |

#### REQ: numbers-are-configuration

No price, allowance, weight of a model, count of places, ceiling or length of a grace is part of this contract. Each is set by configuration on the server, and a client takes it from a response. A number in a fixture is made up and says nothing about any plan, and a model `id` in a fixture is made up and names no real model; a client must not use either as a default. The `message` of a fixture is a placeholder.

## Acceptance Criteria

### AC: show-plan-and-left

Given the answer in `plan-response-pro.json`
When a client shows the plan and the allowance
Then it shows the plan `pro`, `ai.left` of `ai.limit` questions, the reset date from `ai.resetsAt`, and a link to `manageUrl`, and it computes none of them.

### AC: free-without-record

Given a person with no plan record, and the answer in `plan-response-free.json`
When a client shows the plan
Then it shows Free with the numbers of the response, and no link to manage a subscription.

### AC: model-choice

Given the answers in `plan-response-free.json` and `plan-response-pro.json`
When a client shows the choice of model
Then for Free it offers the one model listed, and for Pro the three listed, preselects the one with `default: true`, shows the weight of each, and never offers a model that is not in `ai.models`; the request it sends names the chosen `id` in `model`, or no model (the default).

### AC: model-weight-and-left

Given the answer in `plan-response-pro-one-left.json` (`ai.left` is 1, one model has weight 3) and the body of `refusal-quota-monthly-weight.json`
When a client shows what the person can ask
Then it does not offer the model of weight 3 as available now, still offers the models of weight 1, and on the refusal of a model whose weight is above `limit.left` it says so and offers a lighter model; it computes none of the numbers.

### AC: model-not-included

Given the answer in `plan-response-free.json` and the body of `refusal-model-class.json` received with its 403
When a client has asked for a model that `ai.models` does not list
Then it tells the person that the plan does not include that model, offers the models of `ai.models` and the own key or endpoint, and counts nothing.

### AC: organisation-member

Given the answer in `plan-response-team-member.json`
When a client shows it to a member of the organisation
Then it shows that the organisation's pool pays, shows `ai.left` of the pool, does not offer an upgrade (`canUpgrade` is `false`) and says to ask an admin of `accountTitle`, and sends `X-AI-Account` with that `accountId` on its AI calls.

### AC: several-accounts

Given the answer in `plan-response-several-accounts.json`, where two entries of `accounts` have `pays: true`
When a client is about to send its first AI call
Then it asks the person once which account should pay, remembers the answer, and sends it as `X-AI-Account` on every `/v0/ai/*` call; it passes `account` to the plan endpoint only with an `accountId` taken from `accounts`.

### AC: bad-account

Given the body of `plan-error-not-a-member.json` received with its 403
When a client has called `GET /v0/datatug/plan` with an `account`
Then it drops that account, asks again without it, and shows the account named by the answer; it does not say whether the account exists.

### AC: blocked-with-questions-left

Given the answers in `plan-response-capped.json` and `plan-response-unverified.json`
When a client shows what is left
Then in both it shows that the next question will be refused, with the reason from `ai.blocked`, and in the second one `ai.left` is not 0 although the question will be refused.

### AC: used-up

Given the answer in `plan-response-used-up.json`, whose `ai.left` is 0 and whose `ai.blocked` is `null`
When a client shows what is left
Then it shows that the included questions are used up until `ai.resetsAt`, offers the own key and, where `canUpgrade` is `true`, a plan, and it does not read `blocked: null` as "a question can be asked": a person can ask a model of weight `w` only when `blocked` is `null` and `left` is at least `w`.

### AC: observing

Given the answer in `plan-response-observing.json`, whose `ai.enforced` is `false`
When a client shows the allowance
Then it shows the numbers as information, does not stop the person from asking because of them, and says nothing about a limit.

### AC: ended-and-past-due

Given the answers in `plan-response-ended.json` and `plan-response-stale.json`
When a client shows the plan
Then for the first it shows Free and that the plan ended, with a link to `upgradeUrl`; for the second it shows the plan in force (`effectivePlan`, Free) and that the last payment failed, with a link to `manageUrl`, as [REQ:status-meaning](#req-status-meaning) says.

### AC: stale-record

Given the answer in `plan-response-stale.json`, whose `plan` is `pro` and whose `effectivePlan` is `free`
When a client shows the plan
Then it shows the effective plan and the effective limits, not the plan of the record.

### AC: refusal-sentence

Given each body of `refusal-quota-monthly-free.json`, `refusal-quota-monthly-member.json`, `refusal-quota-capped.json`, `refusal-quota-daily.json`, `refusal-quota-free-budget.json`, `refusal-unverified.json`, `refusal-model-class.json` and `refusal-too-large.json`
When a client receives it with the status of its row of [REQ:refusal-body](#req-refusal-body)
Then it tells the person what happened, when it resets (where `resetsAt` is present), that their own key works, and where to get a plan (only while `canUpgrade` is `true`; where it is `false`, as in `refusal-quota-monthly-member.json`, it says to ask an admin of `accountTitle`); a client that does not know `limit` shows `error.message`.

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

Given the seven bodies of the `checkout-error-` fixtures
When a page receives each with its status from [REQ:session-errors](#req-session-errors)
Then it shows the person a sentence for it, offers a retry only for `offer_busy`, offers sign-in only for `sign_in_required`, and shows the thank-you for `already_subscribed` only when the body carries a `sessionId` (`checkout-error-already-subscribed.json` does, `checkout-error-already-subscribed-no-session.json` does not).

### AC: new-organisation-purchase

Given the answer in `checkout-session-answer-organisation-new.json`, which has no `accountId`
When the payment form completes
Then the page does not name an account yet, polls `GET /v0/datatug/plan` until an organisation entry of `accounts` that was not there before carries the bought plan, and takes the `accountId` of that entry.

### AC: one-question-id

Given a person who sends one message that needs a decision call and several chat calls
When a client makes those calls
Then it sends the same `interactionId` on every one of them, and on a retry of the same message.

### AC: fixtures-intact

Given a copy of `testdata/contract/` and of `contract_test.go` in a client's repository, in the layout of [REQ:fixtures](#req-fixtures)
When its tests run
Then they pass when every fixture is byte for byte the one here, and fail when a fixture is edited, added, removed or not listed in `CHECKSUMS`.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
