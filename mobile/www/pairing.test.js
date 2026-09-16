import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";
import {
  decodeEnvelope, parsePairing,
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

  it("tolerates whitespace around and inside a pasted code", () => {
    const code = envelope("https://moa.example", PAYLOAD);
    const wrapped = `${code.slice(0, 9)} \n ${code.slice(9, 24)} \n ${code.slice(24, 48)} ${code.slice(48)}`;
    expect(decodeEnvelope(`  ${wrapped}  \n`))
      .toEqual({ origin: "https://moa.example", payload: PAYLOAD });
  });

  it("tolerates straight or smart quotes added around a pasted code", () => {
    const code = envelope("https://moa.example", PAYLOAD);
    expect(decodeEnvelope(`"${code}"`)).toEqual({ origin: "https://moa.example", payload: PAYLOAD });
    expect(decodeEnvelope(`“${code}”`)).toEqual({ origin: "https://moa.example", payload: PAYLOAD });
  });

  it("refuses an envelope whose inner payload is not a moa pairing payload", () => {
    expect(decodeEnvelope(envelope("https://moa.example", "not-a-pairing-payload"))).toBeNull();
  });
});

describe("parsePairing", () => {
  it("accepts the single-line code and removes the old two-line format", () => {
    expect(parsePairing(envelope("https://moa.example", PAYLOAD)))
      .toEqual({ origin: "https://moa.example", payload: PAYLOAD });
    expect(parsePairing(`https://moa.example\n${PAYLOAD}`)).toBeNull();
    expect(parsePairing("nonsense")).toBeNull();
  });

  it("keeps the HTML field and parser on the same single-line contract", () => {
    const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
    const field = html.match(/<input\s+id="text"[^>]*>/)?.[0];
    expect(field).toBeDefined();
    expect(field).toContain('type="text"');
    expect(field).not.toContain("multiple");
    expect(parsePairing(envelope("https://moa.example", PAYLOAD))).not.toBeNull();
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
