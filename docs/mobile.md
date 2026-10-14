# Native app

Moa has a native app for iOS and Android. It is the web UI of your own
`moa serve`, in a native shell: the app has no moa of its own and loads the
interface from the server you pair it with, so updating moa updates what the app
shows. The shell adds what a browser tab or an installed web app cannot give a
phone: the whole screen on iOS, push notifications with the content encrypted
end to end, a share sheet, a camera scanner for pairing and, on iOS, passwords kept
behind Face ID. It is not [Pulse](./pulse.md), the voice companion, which is a
separate app.

How to build and sign the apps is in [`mobile/README.md`](../mobile/README.md).
Requirements: iOS 15.5 or later; Android 8 (API 26) or later. The Android app is
built from source for now (see `mobile/README.md`); it is not on Google Play
and does not update itself. An APK is available only when a release includes
one.

## Pairing

A new app asks to be paired before anything else. In the web UI open **Settings →
Devices → Pair a device…** (or the palette's **Pair Pulse…**), then scan the QR
code with the app, or paste the code if the camera is not an option. The code
carries the server's address and works once; from then on the app holds
a credential of its own, which you can revoke from the same Devices page. The
server must be reachable over HTTPS (a Tailscale address, for example); the app
refuses cleartext connections and opens any other address in the system browser.

A paired app has the same reach as your browser, including
[managing provider sign-ins](./serve.md#provider-sign-ins). It is a way for you
to reach your own moa from another device, not a lower-privilege guest.

## Notifications

The app receives the [notifications](./serve.md#push-notifications) moa decides
are worth sending: a question or permission waiting for you, a run that finished
or failed, an owner's digest. Tapping one opens that session in the app. They
travel through a small relay that sees only an opaque envelope, never the title,
session or project, which only your app can decrypt. Turn them on in the app's
Settings; on Android 13 and later the system asks for permission when you do.

## Sharing into moa

In the system share sheet choose moa from any app: a link, text, an image, a PDF
or another file. The sheet lists your project owners first and then your most
recent conversations (searchable), takes an optional note, and **Send** puts the
share in the conversation you picked as a message, reopening it if it was
closed. Nothing is sent before you tap **Send**, and **Cancel** leaves nothing
behind.

- At most 8 files and 32 MB per share, and 256 KB of text.
- If moa cannot be reached or the app is not paired, the sheet offers **Save for
  later**. The next time you open moa, the same conversation picker appears;
  choosing one puts the text and files in its composer **without sending**.
- If a send may have reached the server but the answer never came back, the iOS
  sheet says so and tells you to check the conversation instead of retrying.
  Android does the same when the connection broke after the request was
  written, but a server error answer can still offer Retry, so look at the
  conversation before resending.

## Saved passwords (iOS)

When the agent asks for a password for one action (see [Passwords for one
action](./tools.md#passwords-for-one-action)), the card in the iOS app can
**Save on this iPhone**. The value stays in the iPhone's Keychain behind Face ID,
tied to the paired server, and is not synced. **Settings → Saved passwords**
lists them and forgets any of them; unpairing forgets all of them.

## Android specifics

- The Back button is handled by the page first (closing a sheet, leaving a
  conversation); when the page has nothing to go back to, the app goes to the
  background instead of closing.
- The pairing scanner decodes on the device, with no Google service involved.
  Notifications are meant to be delivered through Firebase Cloud Messaging, and
  carry only the encrypted envelope; the app decrypts it itself. They work only
  if the push relay you use has Android set up; otherwise the app reports
  notifications as unavailable.
- Reinstalling over a build signed with a different key requires uninstalling
  first, which forgets the pairing.
