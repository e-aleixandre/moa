import { describe, expect, test } from "bun:test";
import {
  ACTIVE, EXPIRED, REVOKED,
  countActive, deviceLine, deviceState, devicesValue, expiringSoon, lifeFraction, loadFailure,
  markRevoked, relAge,
  deviceKind, sortDevices, untilLabel,
} from "./devices-model.js";

const DAY = 86400000;
const NOW = Date.parse("2026-09-10T12:00:00Z");

function device(overrides = {}) {
  return {
    id: "aaaaaaaaaaaaaaaaaaaaaaaa",
    label: "moa app (iPhone)",
    issued_at: new Date(NOW - 30 * DAY).toISOString(),
    expires_at: new Date(NOW + 150 * DAY).toISOString(),
    ...overrides,
  };
}

describe("deviceState", () => {
  test("a credential inside its window is active", () => {
    expect(deviceState(device(), NOW)).toBe(ACTIVE);
  });

  test("a past expiry is expired even with no flag from the server", () => {
    expect(deviceState(device({ expires_at: new Date(NOW - DAY).toISOString() }), NOW)).toBe(EXPIRED);
  });

  test("revoked wins over expired: it is what the owner did, not what time did", () => {
    const both = device({
      expires_at: new Date(NOW - DAY).toISOString(),
      revoked_at: new Date(NOW - 2 * DAY).toISOString(),
    });
    expect(deviceState(both, NOW)).toBe(REVOKED);
  });
});

describe("sortDevices", () => {
  test("active first, then most recently seen, and the input is not mutated", () => {
    const stale = device({ id: "stale", last_used_at: new Date(NOW - 5 * DAY).toISOString() });
    const fresh = device({ id: "fresh", last_used_at: new Date(NOW - 60000).toISOString() });
    const gone = device({ id: "gone", revoked_at: new Date(NOW - DAY).toISOString() });
    const input = [gone, stale, fresh];
    expect(sortDevices(input, NOW).map((d) => d.id)).toEqual(["fresh", "stale", "gone"]);
    expect(input.map((d) => d.id)).toEqual(["gone", "stale", "fresh"]);
  });

  test("a device that has never been used falls back to when it was paired", () => {
    const never = device({ id: "never", issued_at: new Date(NOW - 60000).toISOString() });
    const used = device({ id: "used", last_used_at: new Date(NOW - 10 * DAY).toISOString() });
    expect(sortDevices([used, never], NOW).map((d) => d.id)).toEqual(["never", "used"]);
  });
});

describe("countActive", () => {
  test("a revoked credential is not access, so it is not counted", () => {
    const list = [device(), device({ revoked_at: new Date(NOW - DAY).toISOString() })];
    expect(countActive(list, NOW)).toBe(1);
  });

  test("no devices is zero, not a crash", () => {
    expect(countActive(null, NOW)).toBe(0);
  });
});

describe("relAge", () => {
  test("says how long ago in the product's short form", () => {
    expect(relAge(new Date(NOW - 30000).toISOString(), NOW)).toBe("just now");
    expect(relAge(new Date(NOW - 5 * 60000).toISOString(), NOW)).toBe("5m ago");
    expect(relAge(new Date(NOW - 3 * 3600000).toISOString(), NOW)).toBe("3h ago");
    expect(relAge(new Date(NOW - 2 * DAY).toISOString(), NOW)).toBe("2d ago");
    expect(relAge(new Date(NOW - 21 * DAY).toISOString(), NOW)).toBe("3w ago");
    expect(relAge(new Date(NOW - 90 * DAY).toISOString(), NOW)).toBe("3mo ago");
  });

  test("an absent time yields nothing rather than an invented hour", () => {
    expect(relAge(undefined, NOW)).toBe("");
    expect(relAge(null, NOW)).toBe("");
  });
});

describe("untilLabel", () => {
  test("counts down in days, the unit the 180-day credential is acted on in", () => {
    expect(untilLabel(new Date(NOW + 150 * DAY).toISOString(), NOW)).toBe("150 days");
    expect(untilLabel(new Date(NOW + DAY).toISOString(), NOW)).toBe("1 day");
    expect(untilLabel(new Date(NOW + 5 * 3600000).toISOString(), NOW)).toBe("5h");
    expect(untilLabel(new Date(NOW + 60000).toISOString(), NOW)).toBe("under an hour");
    expect(untilLabel(new Date(NOW - 60000).toISOString(), NOW)).toBe("expired");
  });
});

describe("lifeFraction", () => {
  test("is the share of its own window spent, not a share of 180 days", () => {
    const short = device({
      issued_at: new Date(NOW - 5 * DAY).toISOString(),
      expires_at: new Date(NOW + 5 * DAY).toISOString(),
    });
    expect(lifeFraction(short, NOW)).toBeCloseTo(0.5, 5);
  });

  test("clamps rather than drawing a meter past its own end", () => {
    const over = device({
      issued_at: new Date(NOW - 10 * DAY).toISOString(),
      expires_at: new Date(NOW - DAY).toISOString(),
    });
    expect(lifeFraction(over, NOW)).toBe(1);
  });
});

describe("expiringSoon", () => {
  test("marks the fortnight before expiry, and nothing already expired", () => {
    expect(expiringSoon(device({ expires_at: new Date(NOW + 10 * DAY).toISOString() }), NOW)).toBe(true);
    expect(expiringSoon(device({ expires_at: new Date(NOW + 20 * DAY).toISOString() }), NOW)).toBe(false);
    expect(expiringSoon(device({ expires_at: new Date(NOW - DAY).toISOString() }), NOW)).toBe(false);
  });
});

describe("deviceLine", () => {
  test("an active device says when it was last seen", () => {
    expect(deviceLine(device({ last_used_at: new Date(NOW - 2 * 60000).toISOString() }), NOW))
      .toBe("Last used 2m ago");
  });

  test("a device never used says when it was paired instead of inventing a use", () => {
    expect(deviceLine(device({ issued_at: new Date(NOW - 3 * DAY).toISOString() }), NOW))
      .toBe("Paired 3d ago");
  });

  test("a revoked device says so, over anything else it could say", () => {
    const gone = device({
      last_used_at: new Date(NOW - 60000).toISOString(),
      revoked_at: new Date(NOW - 30000).toISOString(),
    });
    expect(deviceLine(gone, NOW)).toBe("Revoked just now");
  });
});

describe("deviceKind", () => {
  test("reads the label the native client actually sends", () => {
    expect(deviceKind("moa app (iPhone)")).toBe("phone");
    expect(deviceKind("moa app (iPad)")).toBe("tablet");
  });

  test("an unrecognised label gets the neutral glyph rather than a guess", () => {
    expect(deviceKind("kitchen radio")).toBe("unknown");
    expect(deviceKind("")).toBe("unknown");
    expect(deviceKind(undefined)).toBe("unknown");
  });
});

describe("devicesValue", () => {
  test("the row reads the count of what still has access, singular when it is one", () => {
    const one = [device()];
    const three = [device({ id: "a" }), device({ id: "b" }), device({ id: "c" })];
    expect(devicesValue(one, true, false, NOW)).toBe("1 device");
    expect(devicesValue(three, true, false, NOW)).toBe("3 devices");
  });

  test("nothing paired says None rather than a zero", () => {
    expect(devicesValue([], true, false, NOW)).toBe("None");
  });

  test("a revoked credential is not counted, because it is not access", () => {
    const list = [device({ id: "live" }), device({ id: "gone", revoked_at: new Date(NOW - DAY).toISOString() })];
    expect(devicesValue(list, true, false, NOW)).toBe("1 device");
  });

  test("not read yet is null, so the row uses the sheet's own loading state", () => {
    expect(devicesValue([], false, false, NOW)).toBe(null);
  });

  test("a failed read says nothing rather than 'None', which would be a lie", () => {
    expect(devicesValue([], true, true, NOW)).toBe("—");
  });
});

describe("markRevoked", () => {
  test("stamps the one device and leaves the list's order alone", () => {
    const list = [device({ id: "a" }), device({ id: "b" }), device({ id: "c" })];
    const next = markRevoked(list, "b", NOW);
    expect(next.map((d) => d.id)).toEqual(["a", "b", "c"]);
    expect(deviceState(next[1], NOW)).toBe(REVOKED);
    expect(deviceState(next[0], NOW)).toBe(ACTIVE);
  });

  test("never mutates the list it was given, which is also the current render's", () => {
    const list = [device({ id: "a" })];
    markRevoked(list, "a", NOW);
    expect(list[0].revoked_at).toBeUndefined();
  });

  test("a device already revoked keeps the time it was revoked at", () => {
    const at = new Date(NOW - 5 * DAY).toISOString();
    const list = [device({ id: "a", revoked_at: at })];
    expect(markRevoked(list, "a", NOW)[0].revoked_at).toBe(at);
  });

  test("an id that is not in the list changes nothing", () => {
    const list = [device({ id: "a" })];
    expect(markRevoked(list, "missing", NOW)[0].revoked_at).toBeUndefined();
  });
});

describe("loadFailure", () => {
  // The 403 is policy, not breakage: GET /api/pulse/devices is owner-only
  // (route_auth.go, routeOwnerAdmin; pulse_pairing_test.go:378 pins it), so a
  // paired phone is refused by design and the page must not cry error.
  test("a 403 says where the question is answered instead of reporting a fault", () => {
    const failure = loadFailure({ status: 403 });
    expect(failure.kind).toBe("forbidden");
    expect(failure.title).not.toMatch(/error|fail/i);
    expect(failure.detail).toMatch(/token/i);
  });

  test("a 503 names the server's own unavailability", () => {
    expect(loadFailure({ status: 503 }).kind).toBe("unavailable");
  });

  test("anything else, including no status at all, is the plain error", () => {
    expect(loadFailure({ status: 500 }).kind).toBe("error");
    expect(loadFailure(new Error("network")).kind).toBe("error");
    expect(loadFailure(undefined).kind).toBe("error");
  });
});
