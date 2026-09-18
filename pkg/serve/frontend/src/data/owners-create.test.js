import { expect, test } from "bun:test";
import { createFailure } from "../components/Owners/Owners.jsx";

// The refusal copy says WHAT TO DO. Both statuses are ones the API actually
// returns (pkg/serve/owners.go), and both are recoverable by the user.

test("409 with open sessions tells you to close them", () => {
  const fail = createFailure(Object.assign(new Error("409: this project has open sessions: close or let the 2 open sessions of this project finish before creating its owner"), { status: 409 }));
  expect(fail.title).toBe("This project still has sessions open.");
  expect(fail.detail).toContain("Close them");
});

test("409 for an existing owner points at the list instead", () => {
  const fail = createFailure(Object.assign(new Error("409: this codebase already has an owner"), { status: 409 }));
  expect(fail.title).toBe("This project already has an owner.");
  expect(fail.detail).toContain("One owner per codebase");
});

test("400 keeps the server's own words rather than inventing a diagnosis", () => {
  const fail = createFailure(Object.assign(new Error("400: owner root: /nope is not a directory"), { status: 400 }));
  expect(fail.detail).toBe("owner root: /nope is not a directory");
});

test("anything else still says the owner was not created", () => {
  const fail = createFailure(new Error("network down"));
  expect(fail.title).toBe("The owner was not created.");
  expect(fail.detail).toBe("network down");
});
