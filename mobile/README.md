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

A code can also be typed by hand, because a camera is not always an option.

It loads the interface from that moa rather than bundling it, so a new version
of moa does **not** need a new build of the app.

## Building it

Requires a Mac with Xcode. From this directory:

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
PDFs and general files, copies the share into an App Group, and closes only
after showing whether the copy succeeded. The extension deliberately does not
try to launch its containing app through responder-chain tricks, which Apple
does not support for share extensions; it tells the user to open moa instead.
Opening moa then presents the same conversation picker used by the web share
target. Choosing a conversation puts the text and files in its composer; it
never sends automatically.

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

1. Select the blue **App** project, then the **App project** (not a target) and
   open **Build Settings**. Switch the filter to **All** and **Combined**.
2. Set `MOA_APP_GROUP_IDENTIFIER` for Debug and Release to an App Group you own,
   such as `group.YOUR_REVERSED_DOMAIN.moa`. Use the same value everywhere.
3. Set `MOA_SHARE_EXTENSION_BUNDLE_IDENTIFIER` for Debug and Release to a
   unique bundle id you own, normally the app id plus `.ShareExtension`.
4. Select the **App** target, open **Signing & Capabilities**, choose your Team,
   leave automatic signing enabled, add **App Groups**, create/select the group
   from step 2, and make sure it is checked.
5. Select the **ShareExtension** target and repeat step 4 with the same Team and
   the same checked App Group. Xcode must produce a separate provisioning
   profile for this target's bundle id.
6. Select the **App** scheme and a physical iPhone, then press **Run**. The app
   target already embeds `ShareExtension.appex`; do not create or embed another
   extension by hand.

If Xcode writes account-specific values into `project.pbxproj` or either
entitlements file while configuring signing, keep those changes local. Do not
commit certificates, provisioning profiles, team ids, or real App Group ids.

`npx cap sync ios` may rewrite `ios/App/CapApp-SPM/Package.swift` and the ignored
copy of `www/`; it must leave the `ShareExtension` target, both entitlements and
the embed phase intact. Review that explicitly after a Capacitor major upgrade.

### Device checks

Use a physical device for the final check (the simulator does not reproduce all
share providers):

1. With moa paired and showing its server frontend, share a Safari URL to
   **moa**. The extension must say **Saved to moa** and **Open moa to choose a
   conversation**. Open moa, choose a conversation, and verify the URL is in the
   composer but has not been sent.
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

Pairing itself currently has the separately known CORS/credential defects in
`www/boot.js` and the server claim route. Share-in does not attempt to fix them:
it retains content independently of pairing, but a fresh install cannot
complete the end-to-end destination-picker check until that work lands. An
already paired installation is expected to work after the native origin is
recorded on its next local boot; there has not yet been a distributed iOS build,
so this is not a migration path for an existing release.

## Why the iOS project is committed

The Android project is disposable Capacitor output. The iOS project is not any
more: share-in adds an app extension target, target membership, entitlements and
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

`node_modules/`, the generated Android project, iOS build products/user data,
and the copy of `www/` produced inside the Xcode project. The committed iOS
project is updated in place with `npx cap sync ios`; it is never regenerated
with `npx cap add ios`.
