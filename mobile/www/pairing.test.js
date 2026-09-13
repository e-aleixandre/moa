import { describe, it, expect } from "bun:test";
import {
  decodeEnvelope, readManual, parsePairing,
  storedServer, rememberServer, forgetServer,
} from "./pairing.js";

// The envelope exactly as the web frontend builds it (data/pulse-pairing.js).
function envelope(serverURL, payload) {
  const json = JSON.stringify({ server_url: serverURL, pairing_payload: payload });
  const b64 = Buffer.from(json, "utf8").toString("base64")
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return "moa-link-v1:" + b64;
}

const PAYLOAD = "moa-pair-v1:abc123:s3cr3t";

function storage() {
  const map = new Map();
  return {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, v),
    removeItem: (k) => map.delete(k),
  };
}

describe("decodeEnvelope", () => {
  it("reads what the web frontend produced", () => {
    const result = decodeEnvelope(envelope("https://moa.example", PAYLOAD));
    expect(result).toEqual({ origin: "https://moa.example", payload: PAYLOAD });
  });

  it("keeps only the origin", () => {
    const result = decodeEnvelope(envelope("https://moa.example/some/path", PAYLOAD));
    expect(result.origin).toBe("https://moa.example");
  });

  // A pairing code that can point the app at plain http is a pairing code that
  // can be downgraded on a hostile network: the app would then send its
  // credential in the clear, to whoever answered.
  it("refuses a code that would bind the app over http", () => {
    expect(decodeEnvelope(envelope("http://moa.example", PAYLOAD))).toBeNull();
  });

  it("refuses anything that is not an envelope", () => {
    expect(decodeEnvelope("https://moa.example")).toBeNull();
    expect(decodeEnvelope("moa-link-v1:not-base64!!")).toBeNull();
    expect(decodeEnvelope("")).toBeNull();
    expect(decodeEnvelope(null)).toBeNull();
  });

  it("refuses an envelope missing either half", () => {
    expect(decodeEnvelope(envelope("https://moa.example", ""))).toBeNull();
    expect(decodeEnvelope(envelope("", PAYLOAD))).toBeNull();
  });
});

describe("readManual", () => {
  it("reads the two lines the pairing panel copies", () => {
    expect(readManual(`https://moa.example\n${PAYLOAD}`))
      .toEqual({ origin: "https://moa.example", payload: PAYLOAD });
  });

  it("tolerates stray whitespace and blank lines", () => {
    expect(readManual(`  https://moa.example  \n\n  ${PAYLOAD}  \n`))
      .toEqual({ origin: "https://moa.example", payload: PAYLOAD });
  });

  it("holds the same https rule as the scanned form", () => {
    expect(readManual(`http://moa.example\n${PAYLOAD}`)).toBeNull();
  });

  it("refuses a single line", () => {
    expect(readManual("https://moa.example")).toBeNull();
  });
});

describe("parsePairing", () => {
  it("accepts either shape without being told which", () => {
    expect(parsePairing(envelope("https://moa.example", PAYLOAD)).origin)
      .toBe("https://moa.example");
    expect(parsePairing(`https://moa.example\n${PAYLOAD}`).origin)
      .toBe("https://moa.example");
    expect(parsePairing("nonsense")).toBeNull();
  });
});

describe("the remembered server", () => {
  it("survives a round trip", () => {
    const s = storage();
    rememberServer("https://moa.example", s);
    expect(storedServer(s)).toBe("https://moa.example");
    forgetServer(s);
    expect(storedServer(s)).toBeNull();
  });

  // Storage is not a trust boundary, but it is the address every request goes
  // to afterwards: a value that got in another way is still checked.
  it("refuses to return a stored http origin", () => {
    const s = storage();
    s.setItem("moa-server", "http://moa.example");
    expect(storedServer(s)).toBeNull();
  });

  it("copes with storage that is unavailable", () => {
    const broken = {
      getItem: () => { throw new Error("denied"); },
      setItem: () => { throw new Error("denied"); },
      removeItem: () => { throw new Error("denied"); },
    };
    expect(storedServer(broken)).toBeNull();
    expect(rememberServer("https://moa.example", broken)).toBe(false);
    expect(() => forgetServer(broken)).not.toThrow();
  });
});
