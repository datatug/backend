# DataTug GitHub actor authorization

This package implements server-side GitHub App user authorization for the
existing DataTug App (`datatug`, App ID `5223634`). Browser code starts the
OAuth redirect and receives only the callback result; the access and rotating
refresh tokens stay encrypted under the Firebase user's DataTug extension
record. Repository operations must obtain a fresh `AuthorizedGitHubRepository`
from `Provider.AuthorizeRepository` for each request.

## Operator setup

Before enabling the provider:

1. Configure the existing DataTug GitHub App (ID `5223634`) with repository
   **Contents: read and write**. Its currently observed public permissions are
   empty, so repository operations remain denied until the App permission is
   configured and affected installations accept the updated permission.
2. Enable user-to-server authorization with expiring user access tokens and
   refresh tokens. Keep the App's client secret server-side.
3. Register `https://datatug.app/github/callback` as the App callback URL and
   provide that same URL as `DATATUG_GITHUB_APP_CALLBACK_URL`. The browser must
   remove the short-lived `code` and `state` query values before analytics,
   then send them in the authenticated completion request to the Cloud API.
4. Configure server-side names only: `DATATUG_GITHUB_APP_ID` (must be
   `5223634`), `DATATUG_GITHUB_APP_CLIENT_ID`,
   `DATATUG_GITHUB_APP_CLIENT_SECRET`, `DATATUG_GITHUB_APP_PRIVATE_KEY` (for
   the dedicated App installation authorizer), `DATATUG_GITHUB_APP_CALLBACK_URL`,
   and the existing `SNEAT_PLATFORM_CRYPTO_KEY` (32 decoded bytes).

Do not reuse OpenVaultDB's App identity, grants, or credential records. Missing
or invalid DataTug configuration must leave the feature unavailable. GitHub
tokens stay server-side and encrypted. The one-time OAuth code/state and PKCE
challenge are protocol values; never log them, persist them in project files,
or retain the callback query after the browser captures it.
