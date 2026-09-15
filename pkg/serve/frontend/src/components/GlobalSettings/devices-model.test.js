import { describe, expect, test } from "bun:test";
import {
  ACTIVE, EXPIRED, REVOKED,
  countActive, deviceLine, deviceState, expiringSoon, lifeFraction, relAge,
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
