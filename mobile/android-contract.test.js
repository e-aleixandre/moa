import { describe, expect, it } from "bun:test";
import { readFileSync, readdirSync } from "node:fs";

// Source contracts for the Android container, in the same style as the iOS
// ones in www/native-auth.test.js: the properties a refactor could silently
// lose, checked where a Linux CI can check them. Behaviour (crypto, origin
// table, navigation, Back) is pinned by the JVM tests in android/app/src/test.

const javaDir = "android/app/src/main/java/com/ealeixandre/moa/app";
const java = Object.fromEntries(
  readdirSync(javaDir)
    .filter((name) => name.endsWith(".java"))
    .map((name) => [name, readFileSync(`${javaDir}/${name}`, "utf8")]),
);
const manifest = readFileSync("android/app/src/main/AndroidManifest.xml", "utf8");
const extraction = readFileSync("android/app/src/main/res/xml/data_extraction_rules.xml", "utf8");
const network = readFileSync("android/app/src/main/res/xml/network_security_config.xml", "utf8");
const appGradle = readFileSync("android/app/build.gradle", "utf8");
const styles = readFileSync("android/app/src/main/res/values/styles.xml", "utf8");
const shareLayout = readFileSync("android/app/src/main/res/layout/activity_share.xml", "utf8");
const rootIgnore = readFileSync("../.gitignore", "utf8");

function code(source) {
  return source.replace(/\/\/.*$/gm, "").replace(/\/\*[\s\S]*?\*\//g, "");
}

describe("Android native bridges", () => {
  it("never exposes a Java object to every origin", () => {
    for (const [name, source] of Object.entries(java)) {
      expect(`${name}: ${code(source).includes("addJavascriptInterface(")}`).toBe(`${name}: false`);
      expect(`${name}: ${code(source).includes("@JavascriptInterface")}`).toBe(`${name}: false`);
    }
  });

  it("installs the bridges as origin-scoped listeners and document-start scripts, or not at all", () => {
    const bridges = code(java["WebBridges.java"]);
    expect(bridges).toContain("WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)");
    expect(bridges).toContain("WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)");
    expect(bridges).toContain("Collections.singleton(OriginPolicy.LOCAL_ORIGIN)");
    expect(bridges).toContain("WebViewCompat.addWebMessageListener(webView, PUSH_PORT, rules");
    expect(bridges).toContain("if (!isMainFrame");
    expect(bridges).toContain("OriginPolicy.mayCall(bridge, method, caller)");
    expect(bridges).not.toMatch(/singleton\("\*"\)|"\*"/);
    // Same JS API as iOS.
    for (const method of ["claim", "authorize", "reauthorize", "reset"]) {
      expect(bridges).toContain(`${method}: (`);
    }
    for (const method of ["status", "enable", "disable", "setVisibleSession"]) {
      expect(bridges).toContain(`${method}: (`);
    }
  });

  it("installs the bridges before Capacitor loads the first page", () => {
    const activity = code(java["MainActivity.java"]);
    expect(activity.indexOf("bridges.install()")).toBeGreaterThan(-1);
    expect(activity.indexOf("bridges.install()")).toBeLessThan(activity.indexOf("super.load()"));
  });

  // Changing WebMessageListeners under a live page breaks that page's port
  // (QA: the authorize after a first claim never arrived, and the next
  // message crashed WebView). Re-scoping happens only as a main-frame
  // navigation starts, or before the first page.
  it("re-scopes the paired bridges only at a navigation boundary", () => {
    const callers = Object.entries(java)
      .filter(([, source]) => code(source).includes("syncPairedOrigin"))
      .map(([name]) => name)
      .sort();
    expect(callers).toEqual(["MainActivity.java", "WebBridges.java"]);
    expect(code(java["MainActivity.java"])).toContain("bridges::syncPairedOrigin");
    const client = code(java["MoaWebViewClient.java"]);
    const override = client.slice(client.indexOf("shouldOverrideUrlLoading"), client.indexOf("static void openExternally"));
    expect(client.split("beforeNavigation.run()").length - 1).toBe(2);
    expect(override.split("beforeNavigation.run()").length - 1).toBe(2);
  });

  it("removes Capacitor's all-origin JavaScript interfaces it does not use", () => {
    const activity = code(java["MainActivity.java"]);
    const capacitor = "node_modules/@capacitor/android/capacitor/src/main/java/com/getcapacitor/plugin";
    for (const [file, name] of [
      ["CapacitorCookies.java", "CapacitorCookiesAndroidInterface"],
      ["CapacitorHttp.java", "CapacitorHttpAndroidInterface"],
    ]) {
      expect(readFileSync(`${capacitor}/${file}`, "utf8")).toContain(`addJavascriptInterface(this, "${name}")`);
      expect(activity).toContain(`removeJavascriptInterface("${name}")`);
    }
  });
});

describe("Android device credential", () => {
  it("lives only in a Keystore-encrypted no-backup file", () => {
    const store = code(java["DeviceCredentialStore.java"]);
    const box = code(java["KeystoreBox.java"]);
    expect(store).toContain("new KeystoreBox(");
    expect(box).toContain('"AndroidKeyStore"');
    expect(box).toContain("KeyProperties.BLOCK_MODE_GCM");
    expect(box).toContain("getNoBackupFilesDir()");
    for (const source of [store, box, code(java["DeviceAuthBridge.java"])]) {
      expect(source).not.toContain("getSharedPreferences");
      expect(source).not.toContain("Log.");
      expect(source).not.toContain("System.out");
    }
  });

  it("travels only over native HTTPS without redirects or the WebView cookie jar", () => {
    const http = code(java["NativeHttp.java"]);
    const auth = code(java["DeviceAuthBridge.java"]);
    expect(http).toContain("setInstanceFollowRedirects(false)");
    expect(http).toContain("CookieHandler.setDefault(null)");
    expect(http).toContain('"https".equals(target.getProtocol())');
    expect(auth).toContain('headers.put("Authorization", "Moa-Device " + credential)');
    expect(auth).toContain('"/api/pulse/pairings/claim"');
    expect(auth).toContain('"/api/pulse/device-session"');
    expect(auth).toContain("CookieManager.getInstance()");
    expect(auth).toContain("manager.flush()");
  });

  it("is never backed up or transferred, and nothing goes over cleartext", () => {
    expect(manifest).toContain('android:allowBackup="false"');
    expect(manifest).toContain('android:dataExtractionRules="@xml/data_extraction_rules"');
    expect(manifest).toContain('android:usesCleartextTraffic="false"');
    expect(network).toContain('cleartextTrafficPermitted="false"');
    for (const section of ["cloud-backup", "device-transfer"]) {
      const body = extraction.split(`<${section}>`)[1].split(`</${section}>`)[0];
      for (const domain of ["root", "file", "database", "sharedpref", "device_root", "device_file", "device_database", "device_sharedpref"]) {
        expect(body).toContain(`<exclude domain="${domain}" path="." />`);
      }
    }
  });
});

describe("Android notifications", () => {
  it("receives data-only FCM in a non-exported service", () => {
    expect(manifest).toMatch(/<service\s+android:name="\.MoaMessagingService"\s+android:exported="false">/);
    expect(manifest).toContain("com.google.firebase.MESSAGING_EVENT");
    expect(manifest).toContain('android:name="firebase_messaging_auto_init_enabled"');
  });

  it("builds without google-services.json and never commits it", () => {
    expect(rootIgnore).toContain("mobile/android/app/google-services.json");
    expect(appGradle).toContain("def servicesJSON = file('google-services.json')");
    expect(appGradle).toContain("apply plugin: 'com.google.gms.google-services'");
  });

  it("names its channels in Spanish with an English fallback", () => {
    const es = readFileSync("android/app/src/main/res/values-es/strings.xml", "utf8");
    const en = readFileSync("android/app/src/main/res/values/strings.xml", "utf8");
    expect(es).toContain('<string name="channel_urgent">Preguntas y permisos</string>');
    expect(es).toContain('<string name="channel_active">Terminadas</string>');
    expect(es).toContain('<string name="channel_passive">Resúmenes</string>');
    for (const name of ["channel_urgent", "channel_active", "channel_passive", "push_fallback_body"]) {
      expect(en).toContain(`<string name="${name}">`);
    }
  });
});

describe("Android native voice call", () => {
  const callJava = ["CallAudio", "CallConnection", "CallConnectionService", "CallService", "CallSocket", "CallSystem",
    "AndroidCallHost", "NativeCallBridge", "NativeCallManager", "NativeCalls"];

  // The frontend (voice-call-native.js) feature-detects this object and is the
  // same on iOS and Android: the surface below is NativeVoiceCall.swift's.
  it("exposes the iOS MoaNativeCall v1 surface to the paired origin only", () => {
    const bridges = code(java["WebBridges.java"]);
    const ios = readFileSync("ios/App/App/NativeVoiceCall.swift", "utf8");
    const script = bridges.slice(bridges.indexOf("static String callScript()"));
    expect(bridges).toContain("WebViewCompat.addWebMessageListener(webView, CALL_PORT, rules, callListener())");
    expect(bridges).toContain("WebViewCompat.addDocumentStartJavaScript(webView, callScript(), rules)");
    // Not in the local pairing page's registrations.
    expect(bridges.split("CALL_PORT, local").length).toBe(1);
    for (const member of ["version: 1", "onevent: null", "open(url, opts)", "send(id, text)", "close(id)", "active()",
      "startAudio(id)", "stopAudio(id)", "setMuted(id, muted)", "__event(ev)"]) {
      expect(script).toContain(member);
      expect(ios).toContain(member);
    }
    for (const op of ["open", "send", "close", "active", "startAudio", "stopAudio", "setMuted"]) {
      expect(script).toContain(`op: \\"${op}\\"`);
      expect(ios).toContain(`op: "${op}"`);
    }
    expect(script).toContain("window.top !== window");
    expect(code(java["NativeCallBridge.java"])).toContain('OriginPolicy.mayCall("call", message.op, caller)');
  });

  it("opens only the validated call socket, with the device credential, without redirects", () => {
    const manager = code(java["NativeCallManager.java"]);
    const socket = code(java["CallSocket.java"]);
    expect(manager).toContain("CallUrl.validate(rawUrl, origin)");
    expect(manager).toContain('"Moa-Device " + credential');
    expect(socket).toContain("followRedirects(false)");
    expect(socket).toContain("followSslRedirects(false)");
    expect(socket).toContain('header("Authorization", authorization)');
    expect(socket).not.toContain('"Origin"');
    expect(code(java["CallUrl.java"])).toContain('"wss".equalsIgnoreCase(uri.getScheme())');
    // The credential is not logged, kept or put in an intent by the call code.
    for (const name of callJava) {
      const source = code(java[`${name}.java`]);
      expect(`${name}: ${/\bLog\.|System\.out|printStackTrace|putExtra\([^)]*credential/.test(source)}`).toBe(`${name}: false`);
    }
  });

  it("keeps a call alive with a phoneCall foreground service and registers it with Telecom", () => {
    const service = manifest.slice(manifest.indexOf('android:name=".CallService"'), manifest.indexOf("/>", manifest.indexOf('android:name=".CallService"')));
    expect(service).toContain('android:exported="false"');
    expect(service).toContain('android:foregroundServiceType="phoneCall|microphone"');
    const telecom = manifest.slice(manifest.indexOf('android:name=".CallConnectionService"'), manifest.indexOf("</service>", manifest.indexOf('android:name=".CallConnectionService"')));
    expect(telecom).toContain('android:permission="android.permission.BIND_TELECOM_CONNECTION_SERVICE"');
    expect(telecom).toContain('<action android:name="android.telecom.ConnectionService" />');
    const system = code(java["CallSystem.java"]);
    expect(system).toContain("PhoneAccount.CAPABILITY_SELF_MANAGED");
    expect(system).toContain("telecom.placeCall(");
    const callService = code(java["CallService.java"]);
    expect(callService).toContain("FOREGROUND_SERVICE_TYPE_PHONE_CALL");
    expect(callService).toContain("FOREGROUND_SERVICE_TYPE_MICROPHONE");
    expect(callService).toContain("NotificationCompat.CallStyle.forOngoingCall(");
  });

  // Google: Telecom routes audio; a call must not also drive Bluetooth SCO.
  it("never mixes Telecom routing with Bluetooth SCO", () => {
    for (const name of callJava) {
      expect(`${name}: ${/startBluetoothSco|stopBluetoothSco/.test(code(java[`${name}.java`]))}`).toBe(`${name}: false`);
    }
  });

  it("captures the voice-communication source as 24 kHz mono and plays 24 kHz", () => {
    const audio = code(java["CallAudio.java"]);
    expect(audio).toContain("MediaRecorder.AudioSource.VOICE_COMMUNICATION");
    expect(audio).toContain("AudioAttributes.USAGE_VOICE_COMMUNICATION");
    expect(audio).toContain("AcousticEchoCanceler");
    expect(audio).toContain("NoiseSuppressor");
    expect(code(java["CallProtocol.java"])).toContain("SAMPLE_RATE = 24000");
    expect(code(java["CallProtocol.java"])).toContain("FRAME_BYTES = 480 * 2");
  });

  it("names its notification channel in Spanish with an English fallback", () => {
    const es = readFileSync("android/app/src/main/res/values-es/strings.xml", "utf8");
    const en = readFileSync("android/app/src/main/res/values/strings.xml", "utf8");
    for (const name of ["channel_call", "call_ongoing"]) {
      expect(en).toContain(`<string name="${name}">`);
      expect(es).toContain(`<string name="${name}">`);
    }
  });

  it("ends the call when the pairing changes", () => {
    const auth = code(java["DeviceAuthBridge.java"]);
    expect(auth.split("NativeCalls.bindingChanged()").length - 1).toBe(3);
  });
});

describe("Android permissions and navigation", () => {
  it("declares what the pairing scanner, voice and notifications request at runtime", () => {
    const declared = [...manifest.matchAll(/<uses-permission android:name="android\.permission\.([A-Z_]+)" \/>/g)].map((m) => m[1]).sort();
    expect(declared).toEqual([
      "ACCESS_NETWORK_STATE",
      "CAMERA",
      "FOREGROUND_SERVICE",
      "FOREGROUND_SERVICE_MICROPHONE",
      "FOREGROUND_SERVICE_PHONE_CALL",
      "INTERNET",
      "MANAGE_OWN_CALLS",
      "MODIFY_AUDIO_SETTINGS",
      "POST_NOTIFICATIONS",
      "RECORD_AUDIO",
      "VIBRATE",
    ]);
  });

  // The pairing scanner is ZXing (QRScannerPlugin), as iOS uses AVFoundation:
  // neither ML Kit nor Google's code scanner (play-services-code-scanner)
  // ships in the Android app. The ML Kit Capacitor plugin stays an npm
  // dependency for iOS until it drops it; capacitor.config.json keeps it out
  // of Android.
  it("ships no ML Kit or Google code scanner", () => {
    const config = JSON.parse(readFileSync("capacitor.config.json", "utf8"));
    const settings = readFileSync("android/capacitor.settings.gradle", "utf8");
    const capacitorBuild = readFileSync("android/app/capacitor.build.gradle", "utf8");
    const variables = readFileSync("android/variables.gradle", "utf8");
    expect(config.android.includePlugins).toEqual(["@capacitor/haptics", "@capacitor/share", "@capacitor/status-bar"]);
    for (const source of [appGradle, settings, capacitorBuild, variables, manifest]) {
      expect(source.toLowerCase()).not.toMatch(/mlkit|play-services-code-scanner|play-services-vision|barcode_ui/);
    }
    expect(appGradle).toContain('implementation "com.journeyapps:zxing-android-embedded:');
    for (const source of Object.values(java)) {
      expect(code(source)).not.toMatch(/com\.google\.mlkit|com\.google\.android\.gms\.vision|GmsBarcodeScann/);
    }
    const scanner = code(java["QRScannerPlugin.java"]);
    expect(scanner).toContain('name = "BarcodeScanner"');
    expect(scanner).toContain("Manifest.permission.CAMERA");
    for (const method of ["isSupported", "scan"]) expect(scanner).toContain(`public void ${method}(PluginCall call)`);
    expect(scanner).toContain('put("barcodes", barcodes)');
    expect(code(java["MainActivity.java"]).indexOf("registerPlugin(QRScannerPlugin.class)"))
      .toBeLessThan(code(java["MainActivity.java"]).indexOf("super.onCreate"));
  });

  it("keeps the main frame on the paired and local origins and minimizes at the root", () => {
    const client = code(java["MoaWebViewClient.java"]);
    const activity = code(java["MainActivity.java"]);
    expect(client).toContain("OriginPolicy.navigation(url, credentials.pairedOrigin())");
    expect(client).toContain("Intent.CATEGORY_BROWSABLE");
    expect(activity).toContain("getOnBackPressedDispatcher().addCallback");
    expect(activity).toContain("moveTaskToBack(true)");
    expect(activity).not.toContain("finish()");
    expect(manifest).toContain('android:enableOnBackInvokedCallback="true"');
    expect(manifest).toContain('android:windowSoftInputMode="adjustResize"');
  });
});

describe("Android share-in", () => {
  const activity = () => code(java["ShareActivity.java"]);
  const shareSources = ["ShareActivity.java", "ShareClient.java", "ShareBody.java", "ShareInbox.java", "ShareInboxBridge.java", "ShareLogic.java"];

  it("is a sheet over the sharing app for what iOS accepts", () => {
    const block = manifest.slice(manifest.indexOf('android:name=".ShareActivity"'), manifest.indexOf("</activity>", manifest.indexOf('android:name=".ShareActivity"')));
    expect(block).toContain('android:exported="true"');
    expect(block).toContain('android:excludeFromRecents="true"');
    expect(block).toContain('android:taskAffinity=""');
    expect(block).toContain('android:theme="@style/Theme.Moa.Share"');
    expect(block).toContain('<action android:name="android.intent.action.SEND" />');
    expect(block).toContain('<action android:name="android.intent.action.SEND_MULTIPLE" />');
    for (const type of ["text/*", "image/*", "application/pdf", "*/*"]) {
      expect(block).toContain(`<data android:mimeType="${type}" />`);
    }
    expect(styles).toContain("<item name=\"android:windowIsTranslucent\">true</item>");
  });

  it("never puts the credential in an intent, an extra, a log or the WebView", () => {
    for (const name of shareSources) {
      const source = code(java[name]);
      expect(`${name}: ${/\bLog\.|System\.out|System\.err|printStackTrace/.test(source)}`).toBe(`${name}: false`);
    }
    const sheet = activity();
    expect(sheet).not.toContain("putExtra(");
    expect(sheet).not.toMatch(/\.credential\b/);
    expect(sheet).toContain("ShareClient.forStored(stored, System.currentTimeMillis())");
    const client = code(java["ShareClient.java"]);
    expect(client).toContain('headers.put("Authorization", "Moa-Device " + credential)');
    expect(client).not.toContain("openConnection");
    expect(client).toContain("NativeHttp.send(");
  });

  it("streams the send body with a fixed length instead of building it in memory", () => {
    const http = code(java["NativeHttp.java"]);
    expect(http).toContain("connection.setFixedLengthStreamingMode(body.length())");
    expect(http).toContain("body.writeTo(out)");
    const body = code(java["ShareBody.java"]);
    expect(body).toContain("new FileInputStream(part.file)");
    expect(body).toContain("CHUNK = 3 * 64 * 1024");
  });

  it("asks for a destination and sends nothing until Send", () => {
    const sheet = activity();
    // The only send is the Send button's.
    expect(sheet.split(".send(destination.sessionId, body)").length - 1).toBe(1);
    expect(sheet).toContain("sendButton.setOnClickListener(view -> send())");
    expect(sheet).toContain("boolean enabled = selected() != null && !sending");
    // Back, Cancel and a tap above the sheet all cancel.
    expect(sheet).toContain("getOnBackPressedDispatcher().addCallback");
    expect(sheet).toContain("root.setOnClickListener(view -> cancel())");
    expect(sheet).toContain("cancelButton.setOnClickListener(view -> cancel())");
  });

  it("reads only other apps' content URIs, before the grant ends, with the limits checked first", () => {
    const sheet = activity();
    expect(sheet).toContain("ContentResolver.SCHEME_CONTENT.equals(uri.getScheme())");
    expect(sheet).toContain('authority.startsWith(getPackageName() + ".")');
    expect(sheet).toContain("OpenableColumns.SIZE");
    expect(sheet).toContain("uris.size() > ShareLogic.MAX_FILES");
    expect(code(java["ShareInbox.java"])).toContain("getNoBackupFilesDir()");
  });

  it("keeps every editable field at 16sp or more", () => {
    expect(styles).toMatch(/<style name="Moa.Share.Field" parent="">[\s\S]*?<item name="android:textSize">16sp<\/item>/);
    const fields = shareLayout.match(/<EditText[\s\S]*?\/>/g);
    expect(fields.length).toBe(2);
    for (const field of fields) expect(field).toContain('style="@style/Moa.Share.Field"');
  });

  it("exposes MoaShareInbox with the iOS API, scoped like the other bridges", () => {
    const bridges = code(java["WebBridges.java"]);
    expect(bridges).toContain('WebViewCompat.addWebMessageListener(webView, SHARE_LOCAL_PORT, local, listener("share", share))');
    expect(bridges).toContain('WebViewCompat.addWebMessageListener(webView, SHARE_PAIRED_PORT, rules, listener("share", share))');
    expect(bridges).toContain("WebViewCompat.removeWebMessageListener(webView, SHARE_PAIRED_PORT)");
    for (const method of ["status", "bindServer", "clearServer", "peek", "acknowledge"]) {
      expect(bridges).toContain(`${method}: (`);
    }
    expect(code(java["MainActivity.java"])).toContain("new ShareInboxBridge(this, credentials)");
  });
});

// Re-review e3 P2: the script that opens a tap in place must never navigate
// by itself. A Promise that settles false (or rejects) later hands the
// decision back to Java on the push port, which asks NavigationGate with the
// tap's own generation. Runs the exact expression PushMessages returns.
describe("Android release build", () => {
  const build = code(appGradle);

  it("derives versionCode from the release version, and stays 1 / 1.0 without it", () => {
    expect(build).toContain("def moaVersion = System.getenv('MOA_ANDROID_VERSION')");
    expect(build).toContain("def moaVersionCode = 1");
    expect(build).toContain('def moaVersionName = "1.0"');
    expect(build).toContain("moaVersion =~ /^(\\d+)\\.(\\d+)\\.(\\d+)$/");
    expect(build).toContain("if (minor > 99 || patch > 99)");
    expect(build).toContain("moaVersionCode = major * 10000 + minor * 100 + patch");
    expect(build).toContain("moaVersionName = moaVersion");
    expect(build).toMatch(/versionCode moaVersionCode\s+versionName moaVersionName/);
    expect(build).not.toMatch(/versionCode \d/);
  });

  it("signs only from the environment and keeps no key material in the repo", () => {
    expect(build).toContain("def releaseKeystore = System.getenv('MOA_ANDROID_KEYSTORE')");
    expect(build).toContain("storePassword requiredSigningEnv('MOA_ANDROID_KEYSTORE_PASSWORD')");
    expect(build).toContain("keyAlias requiredSigningEnv('MOA_ANDROID_KEY_ALIAS')");
    expect(build).toContain("keyPassword requiredSigningEnv('MOA_ANDROID_KEYSTORE_PASSWORD')");
    // A missing part fails the build by name instead of falling back.
    expect(build).toMatch(/def requiredSigningEnv = \{ String name ->\s+def value = System\.getenv\(name\)\s+if \(!value\) \{\s+throw new GradleException\("MOA_ANDROID_KEYSTORE is set but \$\{name\} is missing"\)/);
    expect(build).not.toMatch(/(?:keyAlias|keyPassword|storePassword)[^\n]*\?:/);
    expect(build).not.toContain("'moa-release'");
    expect(build).toMatch(/if \(releaseKeystore\) \{\s+signingConfig signingConfigs\.release/);
    expect(build).not.toMatch(/findProperty\('[^']*(?:[Pp]assword|[Kk]eystore|[Aa]lias)/);
  });
});

describe("Android in-place navigation script", () => {
  const source = code(java["PushMessages.java"]);
  const method = source.slice(source.indexOf("static String openInPlaceScript"));
  const expression = method.slice(method.indexOf('return "(() => {"') + "return ".length, method.indexOf(';\n    }'));
  const build = new Function("call", "token", "JSONObject", "WebBridges", `return ${expression};`);

  function run(navigate, port = []) {
    const assigned = [];
    const window = {
      MoaNavigation: { version: 1, openSession: navigate },
      moaNativePushPort: { postMessage: (data) => port.push(JSON.parse(data)) },
    };
    const script = build('nav.openSession("abc")', 7, { quote: JSON.stringify }, { PUSH_PORT: "moaNativePushPort" });
    const returned = new Function("window", "location", `return ${script};`)(window, { assign: (url) => assigned.push(url) });
    return { returned, assigned, port };
  }
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
  const fallback = [{ id: "openFallback", method: "openFallback", options: { token: 7 } }];

  it("hands a Promise that resolves false back to Java instead of navigating", async () => {
    const f = run(() => Promise.resolve(false));
    expect(f.returned).toBe(true);
    await settle();
    expect(f.assigned).toEqual([]);
    expect(f.port).toEqual(fallback);
  });

  it("hands a rejected Promise back to Java instead of navigating", async () => {
    const f = run(() => Promise.reject(new Error("refresh failed")));
    await settle();
    expect(f.assigned).toEqual([]);
    expect(f.port).toEqual(fallback);
  });

  it("leaves a Promise that resolves true alone", async () => {
    const f = run(() => Promise.resolve(true));
    await settle();
    expect(f.assigned).toEqual([]);
    expect(f.port).toEqual([]);
  });

  it("answers a synchronous result to Java's own callback", async () => {
    for (const value of [true, false]) {
      const f = run(() => value);
      expect(f.returned).toBe(value);
      await settle();
      expect(f.assigned).toEqual([]);
      expect(f.port).toEqual([]);
    }
  });
});
