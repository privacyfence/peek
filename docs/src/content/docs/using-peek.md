---
title: Using Peek
description: Upload HTML, share review links, collect comments, and inspect activity.
kicker: peek upload
---

## Upload A Page

Use the CLI when a report or generated page already exists on disk:

```sh
peek upload demo.html
```

New pages are `restricted` by default, so only you and admins can open them until you grant access or change the visibility; add `--visibility public` for a link anyone can open. The response includes a URL and a slug. Share the URL with reviewers; keep the slug for CLI commands such as `stats`, `comments`, `visibility`, and `export`.

Links are made from the page name plus a random six-character suffix, for example /p/q3-plan-k7m2xq, so a page cannot be found by guessing its name. Renaming a page gives it a new suffixed link; old links keep redirecting.

The dashboard is better when you want to paste HTML, choose visibility visually, or manage several pages at once. Its upload card has three modes: a file, pasted HTML, or **Empty page**, where you type a name and click **Create page** to get a page with a minimal HTML document titled with that name; upload versions to it afterwards. The **Pages** list shows each page by title with an icon: a pencil for pages you own, a shield for pages you can edit as an admin, and an eye for restricted pages shared with you (view only). Shared pages come from user grants on your email address and grants to Google groups you belong to, and show no Access, Stats or Delete actions.

<figure>
  <img src="/peek/assets/screenshots/05-dashboard-uploads.png" alt="Peek dashboard with the upload form and uploads table">
  <figcaption>The dashboard combines upload, link copying, stats, and delete actions in one place.</figcaption>
</figure>

## Pick Visibility

Visibility is a review policy, not a storage location.

| Visibility | Who can open it | Good for |
| --- | --- | --- |
| `public` | Anyone with the link | Fast internal sharing where the URL itself is enough control. |
| `signed-in` | Any signed-in Peek user (the whole organisation when Google org sign-in is on; use `restricted` for named people only) | Internal reports that should stay behind the company login surface. |
| `restricted` (default) | Owner, admins, and signed-in users matching a grant | Reports for a named person or Google group. |

Change visibility after upload when the audience changes:

```sh
peek visibility <slug> public
peek visibility <slug> signed-in
peek visibility <slug> restricted
peek rename <slug> "New name"
```

Restricted pages are opened up with grants: `peek grant <slug> alice@example.com` for one person, `peek grant <slug> team@example.com --group` for a Google group, `peek grants <slug>` to list, and `peek revoke <slug> <email>` to remove. Someone signed in without a grant sees a 403 page. When an admin has set up email under **Admin → Email**, each new grantee gets an email with the link (in an allowed email domain, if one is set), and switching a page to restricted emails everyone who already has a grant. Peek does not email you when you grant yourself access, and a failed send is reported without undoing the grant.

In the dashboard, the **Access** button on an upload's row renames the page, changes its visibility and, for restricted pages, adds or removes people and Google groups. Renaming changes the link to follow the new name; old links keep redirecting and are never given to another page.

## Update A Page

Upload a new version of an existing page by slug:

```sh
peek upload demo.html --slug <slug>
peek versions <slug>
```

`/p/<slug>` always serves the latest version and `/p/<slug>/v/<n>` serves a specific one, with a banner linking to the latest. Visibility and name stay as they were. Comments belong to the version they were written on.

Anyone who has `/p/<slug>` open sees a new version as soon as it is uploaded: the page reloads by itself. It waits while the tab is in the background or the viewer is writing a comment, so unsent text is never lost. `/p/<slug>/v/<n>` links never reload.

## Add A Backend

A page is one HTML file in a sandboxed iframe, so it cannot send email, keep data or use a secret by itself. If your admin has turned on page backends, you can attach a Docker image to a page version and call it from the page:

```sh
peek upload form.html --slug <slug> --backend ghcr.io/acme/contact-form:1.2 [--backend-port 8080]
peek backend env set <slug> SMTP_PASSWORD --value-stdin
peek backend restart <slug>
peek backend <slug>
peek backend logs <slug>
```

The image must be on a registry prefix the admin allowed. Without `--backend`, a new version of the page keeps the backend of the previous latest version; `peek backend set <slug> <image> [--version <n>|latest] [--port <n>]` and `peek backend remove <slug> [--version <n>|latest]` change one version. Variables are encrypted, never shown again, and apply on the next container start, hence `peek backend restart`. `peek backend env unset <slug> <NAME>` removes one. `PORT`, `PEEK_*` and non-`A-Z0-9_` names are reserved.

In the page, call the backend with `peek.fetch`, which returns a normal `Response`:

```js
const res = await peek.fetch("/api/contact", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ name, email, message }),
});
const result = await res.json();
```

The path starts with `/` and goes to the container; `GET`, `POST`, `PUT`, `PATCH` and `DELETE` are allowed, only the `content-type` and `accept` headers are sent, requests are limited to 1 MiB and responses to 8 MiB, and calls fail after 35 s. Viewers need the same access as to the page, and nothing changes for them. The page cannot use `peek.fetch` after a viewer has clicked a link that navigated the iframe away from it.

The container listens on `$PORT` (default 8080) and has a writable `/data` volume; the rest of its filesystem is read-only. Peek sets `PORT`, `PEEK_BACKEND_TOKEN`, `PEEK_UPLOAD_ID` and `PEEK_VERSION`, and adds `X-Peek-Backend-Token` (the backend must reject requests without it), `X-Peek-Upload-Id`, `X-Peek-Version`, `X-Peek-Viewer` (a stable per-page id for the viewer's browser), `X-Peek-Caller` (`browser` or `token`) and, on signed-in and restricted pages only, `X-Peek-User-Id` and `X-Peek-User-Email` to every request. Deleting a page deletes its data volume. See `examples/random-number` in the repository for the smallest backend, explained line by line, and `examples/contact-form` for a complete one.

Scripts and scheduled agents can call the backend with an API token. Turn it on with `peek backend token-calls on <slug>`, then run `peek backend call <slug> /api/items` or `curl -H "Authorization: Bearer $PEEK_TOKEN" https://peek.example.com/api/uploads/<slug>/versions/latest/svc/api/items`. The call follows the page's access rule, wakes the backend if it is stopped, and reaches it with `X-Peek-Caller: token`. `peek backend data export <slug>` and `peek backend data import <slug> <file>` download and replace the backend's `/data` volume, which server backups do not include. Peek adds `X-Peek-Backend-Token` to every call it relays, so it proves only that the call came through Peek. On a public page anyone can make that call, so the backend must do its own authorization for anything it should not serve to everyone.

## Keep Filters In The Address Bar

A page that has filters, tabs or a selected row can store them in the viewer's address bar, so a copied link reopens the same view. The Peek bridge provides `peek.params` (a read-only `URLSearchParams` of the page's current state) and `peek.setParams`:

```js
const region = peek.params.get("region");
peek.setParams({ region: "eu", q: null }); // merges; null, undefined or "" removes a key
peek.setParams("region=eu&q=sales");       // a string replaces all params
```

Peek mirrors the params into the query string of `/p/<slug>` and hands them back to the page on load, so `peek.params` is ready before the page's own scripts run. Assigning `location.hash` inside the page works too. The query string is limited to 2048 characters; `setParams` returns `false` and changes nothing when it is longer.

## Install A Page As An App

Every page link can be added to a phone's home screen. Peek serves a web app manifest for the latest-version link `/p/<slug>`, so Chrome on Android offers **Install app** and Safari on iOS (Share, then **Add to Home Screen**) opens it full screen. Chrome on iOS cannot install web apps; use Safari. Peek must be served over HTTPS, and viewers of non-public pages sign in again inside the installed app on some iOS versions.

A page chooses its own icon and color from its `<head>`:

```html
<link rel="icon" href="data:image/png;base64,iVBORw0KGgo...">
<meta name="theme-color" content="#0b2f63">
```

The icon must be a `data:` URI (PNG, SVG, JPEG, GIF or WebP), up to 256 KB decoded, because an upload is a single file and Peek does not load remote icon URLs. Use an SVG or a PNG of at least 512x512 for Android installs, and a PNG of at least 180x180 for the iOS home screen, which ignores SVG. A `rel="apple-touch-icon"` data URI is used when there is no `rel="icon"`. Pages without an icon use Peek's. The app name is the page name, and the theme color must be a hex color. The icon also becomes the browser tab favicon.

## Review In Context

Open `/p/<slug>`. Peek keeps the uploaded page full-screen and adds a small toolbar for comments, view count, and the comments panel. Use the chevron button at the end of the toolbar to hide it while reading; press Shift+H to bring it back (or hide it again). Reloading the page always shows the toolbar.

Select text when exact wording matters:

<figure>
  <img src="/peek/assets/screenshots/03-text-anchor.png" alt="Selected text inside a shared report with Peek's inline comment button visible">
  <figcaption>Text comments keep feedback attached to the sentence or phrase under review.</figcaption>
</figure>

Use element comments for sections, charts, cards, headings, or layout issues:

<figure>
  <img src="/peek/assets/screenshots/04-element-pin.png" alt="Peek element comment mode with a feedback composer open on a report section">
  <figcaption>Element comments pin feedback to a rendered part of the page.</figcaption>
</figure>

Use a page comment when the note is about the whole artifact rather than one target.

## Read Feedback

The comments panel shows author, timestamp, scope, target text, and body. Anchored comments are clickable, so owners can jump back to the relevant part of the page. Comments from signed-in Peek accounts show the account name with a "signed in" marker; a signed-in viewer cannot choose another name. Names without the marker were typed by the viewer and are not verified.

<figure>
  <img src="/peek/assets/screenshots/02-viewer-comments.png" alt="Peek shared-page viewer with the comments panel open">
  <figcaption>The panel is the review thread: every note carries enough context to act on it later.</figcaption>
</figure>

Owners can read comments from the CLI:

```sh
peek comments <slug>
peek comments <slug> --version 2
peek export <slug>
```

Use `comments` for quick terminal review. Use `export` when an agent, ticket, or audit trail needs the upload metadata, visits, and comments together.

## Stats

Stats answer a narrow question: did reviewers open the page?

<figure>
  <img src="/peek/assets/screenshots/06-upload-stats.png" alt="Peek stats page with visit totals and recent visits">
  <figcaption>Stats show total visits, unique visitors, and recent visits for one upload.</figcaption>
</figure>

Peek hashes analytics IPs. Treat stats as internal review activity, not a full web analytics product.

## API Keys

Every signed-in account has an **API keys** card on the dashboard. **Create key** takes a name (1 to 100 characters) and shows the full key once, starting with `peek_`, with a Copy button ("Copy it now. Peek stores only a hash and cannot show it again.").

The list shows each key's name, last four characters, created, last used (updated at most once a minute) and expiry, including the "CLI login" keys that `peek login` creates. Keys that were created before the hint was recorded show `—` instead of their last four characters.

**Regenerate** replaces the secret and keeps the name, and the old key stops working at once. **Revoke** deletes the key after a confirmation.

A key acts as your account with the same access, so an admin's key can use the admin API. It keeps working until it is revoked or the account is disabled, even if an allowed email domain is set later. Use it as `PEEK_TOKEN` or as `Authorization: Bearer <key>`. Keys from `peek login` expire 90 days after approval.

## CLI Workflow

Configure once:

```sh
peek login --host https://peek.example.com
```

Common user commands:

```text
peek upload <file.html> --visibility public|signed-in|restricted
peek upload <file.html> --slug <slug>
peek upload <file.html> --backend <image> [--backend-port <n>]
peek backend <slug>
peek backend set|remove|restart|logs <slug> ...
peek backend call <slug> <path>
peek backend token-calls on|off <slug>
peek backend data export|import <slug> ...
peek backend env set|unset <slug> <NAME> ...
peek versions <slug>
peek grant|revoke <slug> <email> [--group]
peek grants <slug>
peek list
peek stats <slug>
peek comments <slug> [--version <n>]
peek export <slug>
peek visibility <slug> public|signed-in|restricted
peek rename <slug> <new name>
peek delete <slug>
peek delete-all
```

For automation, set `PEEK_HOST` and `PEEK_TOKEN`, with a key from **Dashboard → API keys** or from `peek login`. Prefer browser login, `--token-stdin`, or `--token-file` over passing a token directly in the command line.
