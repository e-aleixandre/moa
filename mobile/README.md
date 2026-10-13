# The native container

This is the iOS/Android shell around the frontend that `moa serve` already
ships. It is **not** part of the moa binary: `go build` never looks here, and
`go install github.com/.../moa` produces the same binary whether or not this
directory exists.

## Why there is a native container at all

An installed web app on iOS is not given the whole screen. Measured on an
iPhone 17 Pro: the screen is 402x874, the window a web app receives is 402x812,
and the missing 62px cannot be painted by any CSS (WebKit 313800). The same
platform withholds the share sheet, programmable haptics and pointer control.

None of that is a moa problem and none of it is fixable from the web side. A
native container owns the window, so it can hand those back.

## One design, two levels

The frontend does not have a separate "app version". It reads which shell it is
running in (`src/data/shell.js`) and CSS grants one of two levels
(`src/tokens/shell.css`):

- **base** — browser tab and installed web app. Content stays inside the safe
  area. This must look finished on its own.
- **edge** — native container only. Content runs under the status bar and down
  to the physical bottom.

A web app does **not** get `edge`, however convincingly it reports being
standalone. That is enforced by a test, not by convention.

## Which moa it talks to

The app has no address compiled in. It is the same client as the web one with
no server of its own, so it starts unbound and asks to be paired: open moa in a
browser, create a pairing code, scan it. The code carries the origin and a
one-time payload -- the browser that made it was already talking to the right
server -- and the app keeps a device credential of its own from then on.

The same code can also be pasted by hand, because a camera is not always an
option.

On iOS the scanner is a small local Capacitor plugin
(`ios/App/App/QRScannerPlugin.swift`) on AVFoundation's
`AVCaptureMetadataOutput`. It answers the calls the pairing page makes under the
plugin name `BarcodeScanner` (`isSupported`, `checkPermissions`,
`requestPermissions`, `scan`), so `www/barcode-scanner.js` does not know which
implementation is behind it. It replaced Google's ML Kit plugin, which reports
diagnostics to Google and would have ruled out declaring that the app collects
no data. The camera cannot be exercised in the simulator: it reports the camera
as unsupported, so a real scan needs a device.

It loads the interface from that moa rather than bundling it, so a new version
of moa does **not** need a new build of the app.

### Pairing transport and credential boundary

The pairing claim is made by the iOS container with an ephemeral `URLSession`,
not by the local WebView page. This is deliberate. A narrow CORS policy for
`capacitor://localhost` could make the original JavaScript claim work, but it
would still expose the durable credential to page JavaScript and grant a
browser origin permission to read an unauthenticated secret-bearing response.
The native request needs neither a preflight nor a CORS grant; the server
rejects browser-originated claims instead. Redirects are also disabled so the
one-time secret cannot follow a server redirect.

After a successful claim, iOS stores the server origin, credential and expiry
as a generic-password Keychain item with
`kSecAttrAccessibleWhenUnlockedThisDeviceOnly`. The credential is never put in
a URL, `localStorage`, `UserDefaults`, the App Group, or a log. It is sent only
by native `URLSession`, over HTTPS, to exchange for a 24-hour browser session.

That session is a separate random, signed value in a `Secure`, `HttpOnly`,
`SameSite=Strict`, host-only cookie. WebKit therefore attaches it to both REST
requests and WebSocket upgrades without exposing either secret to JavaScript
or putting one in a WebSocket URL/subprotocol. The server checks the device's
durable revocation and expiry state whenever it accepts the cookie. Existing
device WebSockets retain the same server-side lease and are closed immediately
on revocation. If the short browser session expires, the remote frontend asks
the native bridge to renew it from Keychain; if the device itself was revoked,
the bridge clears its binding and returns to the pairing screen.

## Building it

Requires a Mac with Xcode and CocoaPods. The Capacitor plugins come in through
the committed `Podfile`. From this directory:

    npm install
    npm run assets           # icons and splash -- NOT done by cap sync
    npx cap sync ios
    npx cap open ios

Then select your device in Xcode and press Run. With a paid developer account
the build lasts a year; with a free one, seven days.

`npm run assets` is its own step on purpose, and skipping it is why an early
build wore Capacitor's default icon: `cap sync` copies web assets and native
plugins and does nothing about `resources/`. Run it again whenever the files in
`resources/` change.

`resources/` holds the sources it reads, under the names the generator looks
for -- it ignores anything else:

    icon-only.png        1024x1024  what iOS shows
    icon-background.png  1024x1024  Android adaptive, back layer
    icon-foreground.png  1024x1024  Android adaptive, front layer, inset
                                    because the system crops a circle out of it
    splash.png           2732x2732
    splash-dark.png      2732x2732

`icon.png` is the original artwork the rest are derived from. It is kept
because it is the source, not because anything reads it.

## iOS share-in

The iOS app contains a `ShareExtension` target. It accepts links, text, images,
PDFs and general files. The share sheet lists the paired server's destinations
(`GET /api/share/destinations`: **Owners**, then the 50 most recent
conversations, searchable), takes an optional message, and sends only when the
user taps **Send** (`/send`, preceded by `/resume` when the conversation is
closed). Nothing is sent or queued before that tap; **Cancel** leaves nothing
behind. Files are streamed to a temporary file as base64 and uploaded from disk,
because an extension has roughly 120 MB of memory.

If the server cannot be listed or reached, or the iPhone is not paired, the
sheet says what to do and offers **Save for later**. That copies the share into
an App Group queue; opening moa then presents the same conversation picker used
by the web share target. Choosing a conversation puts the text and files in its
composer; it never sends automatically. A send whose outcome is unknown (a timeout, a lost
connection, or any answer other than an explicit 400/401/403/404/413/426) is neither retried nor saved: the sheet says to open
moa and check the conversation. A note typed in the
sheet is kept when saving, and text over 256 KB is refused before any upload.

The extension reads the device credential, read-only, from the **app's private
keychain access group** (`$(AppIdentifierPrefix)$(MOA_APP_BUNDLE_IDENTIFIER)`),
never the push group that `NotificationService` uses. Inside the extension
`PRODUCT_BUNDLE_IDENTIFIER` is the extension's own, so the app's id is the
project setting `MOA_APP_BUNDLE_IDENTIFIER`, also used for the app target, and
the extension looks the credential up under the app's service name
(`MoaDeviceAuthService`). A fork must change that setting together with the
other bundle identifiers. The extension requests only the server origin stored
with the credential and refuses redirects, like the app.

The App Group is the durable boundary between processes. This matters in two
cases: iOS may terminate the extension immediately after it closes, and the app
may still be on its local pairing screen. An unpaired app leaves every share in
the group and says that it is saved; after pairing, the remote frontend reads
the oldest one through a small native bridge. The bridge is injected into every
main-frame navigation because the frontend comes from the paired server, not
from `www/`. It serves file data only when the requesting HTTPS origin is the
origin recorded by the local pairing page.

The limits intentionally match the existing composer/server boundary: at most
8 files and 32 MB total per share, plus 256 KB of text metadata. A 50 MB PDF or
video is rejected by the extension with an instruction instead of being copied
partially. Up to 20 complete shares may wait; when that queue is full, a new
share is rejected and the user is told to open moa first. Pending shares are not
expired silently. Files cross from native code to the remote page as base64 and
become browser `File` objects. That costs temporary memory, but avoids a custom
URL protocol and stays bounded by the same 32 MB limit.

### First build on a Mac

The repository contains placeholders, not a developer team, certificate,
profile, or real App Group. From a checkout of this branch:

    cd mobile
    npm ci
    npm run assets
    npx cap sync ios
    npx cap open ios

Do **not** run `npx cap add ios`: it replaces the committed project and removes
the extension.

In Xcode, configure these values before building:

1. The two build settings below are **already committed** with this project's
   real identifiers, so there is nothing to type:
   - `MOA_APP_GROUP_IDENTIFIER` = `group.com.ealeixandre.moa`
   - `MOA_SHARE_EXTENSION_BUNDLE_IDENTIFIER` = `com.ealeixandre.moa.app.ShareExtension`

   To read them: select the blue **App** project, then the **App project** (not
   a target), open **Build Settings**, and switch the filter to **All** and
   **Combined**. A fork that is not this account's has to replace both.
2. Select the **App** target, open **Signing & Capabilities**, choose your Team,
   add **App Groups**, and check `group.com.ealeixandre.moa`.
3. Select the **ShareExtension** target and repeat step 2 with the same Team and
   the **same** checked App Group. This target signs under its own bundle id, so
   it gets its own provisioning profile.

   The group must be **identical** in both targets. If they differ, the
   extension writes the share into a container the app never reads, and nothing
   fails loudly: the share is simply never delivered.
4. Select the **App** scheme and a physical iPhone, then press **Run**. The app
   target already embeds `ShareExtension.appex`; do not create or embed another
   extension by hand.

If Xcode writes account-specific values into `project.pbxproj` or either
entitlements file while configuring signing, keep those changes local. Do not
commit certificates, provisioning profiles, team ids, or real App Group ids.

`npx cap sync ios` may rewrite the ignored copy of `www/` and update CocoaPods;
it must leave the `ShareExtension` target, both entitlements and the embed phase
intact. Review that explicitly after a Capacitor major upgrade.

### Device checks

Use a physical device for the final check (the simulator does not reproduce all
share providers):

1. With moa paired, share a Safari URL to **moa**. The sheet must list your
   owners under **Owners** and recent conversations under **Recent**, with
   **Send** disabled. Choose an owner, type a message, tap **Send**: the sheet
   closes and the conversation receives your message and the link. Cancel
   instead and confirm nothing arrived and `ShareInbox` holds no new entry.
   Choose a closed conversation: moa reopens it and delivers the message.
   Turn the server off (or Tailscale), share again: the sheet says it cannot
   reach moa; tap **Save for later**, open moa and place it from the picker.
2. Repeat from Photos with one image and from Files with a PDF and another file
   type. Verify each becomes an attachment chip and can be sent normally.
3. Share while moa is terminated, then launch it. The destination picker must
   still appear. Share two items before opening moa and place/dismiss each in
   order.
4. On a fresh, unpaired install, share an item and then open moa. The pairing
   page must say **Your shared item is saved**. Once pairing succeeds, the item
   must still reach the destination picker.
5. Try a file larger than 32 MB. The extension must reject it visibly; moa must
   not later show a partial attachment.
6. Navigate the web view away from the paired origin, if testing tools allow
   it, and confirm a call to `MoaShareInbox.peek()` is rejected. This checks
   that shared files are not exposed to arbitrary pages.

### Native pairing checks

These checks require a physical device and a real server certificate. They
cannot be completed by the repository's Linux checks. Pairing codes are limited
to five per hour, so create one only after the app is installed and the server
is ready; do not create codes speculatively.

1. Start from an app identity that has never paired, or revoke the existing test
   device and reopen the app until it returns to the pairing screen. Deleting an
   iOS app is not a reliable Keychain reset. Create one pairing code, scan it,
   and verify the server frontend loads. Terminate and reopen the app; it must
   return to that frontend without another code.
2. In Safari's Web Inspector, inspect the app page. `localStorage` must contain
   only the `moa-server` origin, never a value beginning with a device id or
   `moa-pair-v1`. Requests and the session WebSocket should carry the
   `__Host-moa_device` cookie automatically; page JavaScript must not be able to
   read it through `document.cookie`.
3. Revoke this device from moa's device list while a conversation is open. The
   live socket must close, the app must return to **Pair this app**, and it must
   stay there rather than reconnecting. Create one new code only when ready and
   verify re-pairing succeeds.
4. Turn networking off before reopening an already paired app. It must preserve
   the Keychain credential and show a connection error, not silently forget the
   device or consume a new pairing code. Restore networking and reopen it.
5. Repeat the fresh-install pending-share check above. The saved item must
   remain in the App Group across pairing and reach the destination picker once
   the authenticated frontend loads.

There has not yet been a distributed iOS build, so no migration of an existing
release is required.

## Native notifications

The app receives moa's notifications through APNs with end-to-end encrypted
content; the wire contract is `relay/PROTOCOL.md`. A third target,
`NotificationService`, decrypts each alert before iOS shows it. It shares
`PushEnvelope.swift` with the app and reads only the push secret.

Two Keychain access groups keep that boundary:

- `$(AppIdentifierPrefix)com.ealeixandre.moa.app` -- the app's own group,
  listed **first** in `App.entitlements` and named on every credential query.
  Only the app and `ShareExtension` have it (the extension reads the device
  credential to list and send; `NotificationService` cannot).
- `$(AppIdentifierPrefix)$(MOA_PUSH_KEYCHAIN_GROUP)` -- the push secret,
  shared by the app and `NotificationService`, never by `ShareExtension`.

Build settings: `MOA_PUSH_RELAY_URL` (default `https://moa-push-relay.ealeixandre.workers.dev`, provisional until `push.letmoa.run`; must
equal the server's `push.relay_url`), `MOA_PUSH_KEYCHAIN_GROUP`,
`MOA_APP_BUNDLE_IDENTIFIER` and `MOA_NOTIFICATION_SERVICE_BUNDLE_IDENTIFIER`.

The relays the app will talk to are a list, `MoaPushRelayURLs` in `Info.plist`
(currently just `$(MOA_PUSH_RELAY_URL)`). The server says which relay it is
configured for (`push.relay_url`, reported by `GET /api/push/native/status`);
the app uses that relay only if the URL is exactly one of the entries and is a
bare `https` origin, otherwise registration fails with `relay_mismatch`. An
empty or unreadable list fails as `unsupported`. To add a relay such as
`https://push.letmoa.run` in a later app version, add it to the array: servers
configured for the old relay keep working, and new ones can use either. Do not
add an entry the app should not send a push secret to. In Xcode, give the
`NotificationService` target the same Team and the Keychain Sharing capability
with the push group; give `App` Push Notifications, Time Sensitive
Notifications and Keychain Sharing with both groups, private one first.
`PushEnvelopeTests` checks the Swift crypto against `relay/test/vectors.json`.

Test Keychain isolation with a **new** pairing: an existing install keeps its
credential where it was created, so it cannot reveal a regression.

## Privacy declarations

The app carries `App/PrivacyInfo.xcprivacy`: no tracking, no tracking domains,
no collected data types, and one required-reason API, `UserDefaults` (reason
`CA92.1`, state only the app itself reads). The extensions use no required-reason
API -- the App Group is a file container, not shared `UserDefaults` -- so they
have no manifest; add one if that changes. `Info.plist` sets
`ITSAppUsesNonExemptEncryption` to `false`: the container uses HTTPS and Apple's
CryptoKit only. It declares no `UIBackgroundModes`. The pairing screen links the
privacy policy (`https://letmoa.run/privacy`), which opens in the system browser.

The minimum iOS version is 15.5. Capacitor 8 itself needs only 15.0; 15.5 was
raised for ML Kit and has been left as is, because lowering it is a decision
about which devices to support, not a consequence of the scanner change.

## Releasing to the App Store

Store builds are signed and uploaded from the maintainer's Mac, not from CI
(`.github/workflows/ios.yml` is kept coherent but is not the release path).
Every signing secret lives in one dedicated keychain on that Mac, never in the
login keychain, the repository, a chat or a file left on disk:

- the Apple Distribution certificate and its private key;
- the App Store Connect API key (`.p8`), stored as a generic password item
  (service `moa-asc-api-key`, account = the key ID), imported once from the
  file it was downloaded as, after which that file is deleted (Trash included).

The keychain's password is held by the maintainer, who unlocks it for a
release; whoever runs the build never needs it. For each release:

1. The maintainer unlocks the signing keychain.
2. Archive the `App` scheme in Release with manual signing: the Apple
   Distribution identity from that keychain and one App Store provisioning
   profile per target (App, NotificationService, ShareExtension). Raise the
   build number above the last upload.
3. Export with method `app-store-connect`, mapping each bundle id to its
   profile.
4. Read the API key from the keychain into a temporary file with mode 0600,
   upload the `.ipa` with it, and delete that file when the upload ends,
   whether it succeeded or not.
5. Lock the keychain again.

A build goes to TestFlight first and is checked on a real device (pairing,
push in the production APNs environment) before it is submitted for review.
Exporting the `.p12` out of the keychain (for an offline backup) is a decision
for the maintainer, not part of a release.

## Android

Same app id (`com.ealeixandre.moa.app`), same startup: the local pairing page
(`www/`, served by Capacitor at `https://localhost`) until the app is paired,
then the paired server's own frontend over HTTPS. Requires Android 8 (API 26).

### Building

Android Studio or just the SDK (platform 36) and JDK 21. From this directory:

    npm install
    npm run assets           # icons and splash, when resources/ changed
    npx cap sync android
    cd android && ./gradlew assembleDebug testDebugUnitTest

The debug APK is `android/app/build/outputs/apk/debug/app-debug.apk`; install
with `adb install -r`. `npm run android` opens the project in Android Studio.
Debug builds are signed with the SDK's per-machine debug key
(`~/.android/debug.keystore`), so an APK built on another machine has to be
uninstalled before installing over it -- and uninstalling forgets the pairing.
There is no release signing configured yet.

`testDebugUnitTest` runs the JVM tests: the push crypto against every case in
`relay/test/vectors.json` (read from the repo, not copied), the bridge origin
table, navigation, notification channels, the relay confirmation and the
session cookie. `android-contract.test.js` (Bun) pins the security properties
a refactor could lose.

The relay URL is a build property and must equal the server's
`push.relay_url`: `./gradlew assembleDebug -PmoaPushRelayUrl=https://...`
(default `https://moa-push-relay.ealeixandre.workers.dev`).

### Firebase

Notifications use FCM directly from native code (no Capacitor push plugin).
`android/app/google-services.json` comes from the moa Firebase project
(Project settings -> Your apps -> Android app `com.ealeixandre.moa.app`) and is
**not committed**: copy it into place before building. Without it the build
still succeeds, Firebase is not initialised, and the app reports notifications
as unsupported; everything else works. The relay sends data-only messages; the
app decrypts them itself, so Google never sees the content.

### Permissions

| Permission | For |
|---|---|
| `INTERNET`, `ACCESS_NETWORK_STATE` | talking to the paired moa |
| `CAMERA` | scanning the pairing code (ZXing, runtime prompt) |
| `RECORD_AUDIO`, `MODIFY_AUDIO_SETTINGS` | voice messages in the WebView |
| `POST_NOTIFICATIONS` | asked when notifications are turned on (Android 13+) |
| `VIBRATE` | haptics |

### Pairing scanner

The QR scanner is `QRScannerPlugin` (ZXing, via `zxing-android-embedded`),
registered under the same plugin name and with the same four calls as iOS's
`QRScannerPlugin.swift` (`isSupported`, `checkPermissions`,
`requestPermissions`, `scan` -> `{ barcodes: [{ rawValue }] }`, an empty list
when the person goes back), so `www/barcode-scanner.js` is shared. It decodes
on the device; no Google Play services, ML Kit or Firebase code is involved.

The ML Kit npm package is gone; `capacitor.config.json` still lists the Android
plugins (`android.includePlugins`) so `cap sync android` only picks those up.

### Origins and storage

- Inside the app: only `https://localhost` and the exact paired HTTPS origin.
  Anything else opens in the system browser; cleartext is refused everywhere.
- The native bridges (`MoaNativeAuth`, `MoaNativePush`, `MoaShareInbox`) are WebView message
  listeners scoped to those two origins, main frame only, with the same method
  table as iOS. They are never `addJavascriptInterface`; if the WebView lacks
  message listeners or document-start scripts, the bridges are absent and the
  app fails closed.
- The device credential and push secret are encrypted with a non-exportable
  Android Keystore AES-GCM key in the no-backup directory. Backup and
  device-to-device transfer exclude everything.
- Back asks the page (`window.MoaHandleBack()`); when the page does not
  handle it, the app goes to the background instead of closing.

### Share-in

`ShareActivity` receives `ACTION_SEND` / `ACTION_SEND_MULTIPLE` (text, links,
images, PDFs, any file) as a dark sheet over the app that shared. The owner
picks the conversation there: owners first, then recent sessions, from
`GET /api/share/destinations` with `Authorization: Moa-Device`. An optional
note goes first, then the shared text/link/title (deduplicated like the web
share target). Nothing is sent until **Send**; Cancel, Back or a tap above the
sheet sends and keeps nothing. Send is `POST /api/sessions/{id}/send`; a
closed conversation (404) is resumed (`/resume`, 409 also fine) and sent once
more. Attachments are streamed as base64 from disk with a fixed
Content-Length, never built as one string.

Shared files are copied at once into a staging share under the no-backup
directory (`no_backup/ShareInbox/.<id>.tmp`): the content URI grant lasts only
as long as the sheet. Only `content://` URIs from other apps are read, and the
provider-reported size is checked before reading. Limits as iOS: 8 files and
32 MB in total per share, 256 KB of text, 20 shares waiting.

If listing or sending certainly failed (not paired, server error, unreachable
before the request left), the sheet offers **Save for later**, which commits
the staging share, note included, to the queue. If a send may have reached the
server (the connection broke after the request was written), it says so and
offers only **Open moa** and **Close**: saving or retrying could deliver it
twice. The 256 KB text limit applies to exactly what is sent or saved, note
and separator included. moa
places it from its own picker through `window.MoaShareInbox` (same API as
iOS: `status`/`bindServer`/`clearServer` from the local page, `peek` /
`acknowledge` from the paired origin only), a WebMessage listener like the
other bridges.

Manual check: share a link from Chrome and an image from Photos to moa, pick
an owner, add a note, Send; the conversation gets one message with the note,
the link and the image. Pick a closed conversation: it reopens and receives
it. With the server down, Save for later, then open moa: its picker offers
the item.

### Notification channels

| Channel | Level | Behaviour |
|---|---|---|
| Preguntas y permisos | urgent | heads-up, sound |
| Terminadas | active | sound |
| Resúmenes | passive | silent |

Names follow the system language (English otherwise). A message that cannot be
decrypted is shown as a generic alert on «Terminadas».

## Why the native projects are committed

Both are. Android carries its own Java (bridges, credential storage, push), so
`android/` is source like `ios/`: update it with `npx cap sync android`, never
regenerate it with `npx cap add android`. For iOS: share-in adds an app extension target, target membership, entitlements and
an embedding build phase. None of those can be represented by a Capacitor
plugin or a podspec; those mechanisms can add code to an existing target, but
cannot declare a second signed application target.

We considered regenerating `ios/` and patching `project.pbxproj` after every
`cap add ios`. That preserves the old small diff, but moves the Xcode project
format into a custom injector that must be retested whenever either Xcode or
Capacitor changes. It also makes signing failures depend on an easy-to-forget
post-generation step. Committing the project is the boring option: Xcode owns
its own project and `cap sync ios` updates the generated web assets and package
references without deleting the extension.

The cost is that Xcode project changes can conflict. Resolve source/configuration
conflicts normally; do not regenerate the project or run `cap add ios`, because
that would remove the extension. On a Capacitor major upgrade, review the
upstream iOS template and migrate this project in Xcode rather than replacing
it. That explicit review is less maintenance than keeping a project-file
rewriter compatible with an evolving undocumented format.

## What is not committed

`node_modules/`, Gradle/iOS build products and user data, the copies of `www/`
that `cap sync` writes inside both native projects, and
`android/app/google-services.json`. The committed iOS
project is updated in place with `npx cap sync ios`; it is never regenerated
with `npx cap add ios`.
