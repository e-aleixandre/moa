// owner-folder.test.js — where the New owner folder explorer starts, and what
// it says when a folder cannot be listed. The dialog used to open on an empty
// path: no listing was requested and the list read "No subfolders", so the
// only way in was the ".." button, which jumped to "/".

import { expect, test } from "bun:test";
import { folderListing, startingFolder } from "./owner-folder.js";

test("the explorer starts in the configured workspace root", () => {
  expect(startingFolder({ workspaceRoot: "/srv/work", homeDir: "/home/me" })).toBe("/srv/work");
});

test("without a workspace root it starts in the user's home", () => {
  expect(startingFolder({ workspaceRoot: "", homeDir: "/home/me" })).toBe("/home/me");
});

test("with neither, or no capabilities at all, it starts at the root", () => {
  expect(startingFolder({})).toBe("/");
  expect(startingFolder(undefined)).toBe("/");
});

test("a listed folder yields its subfolders and no problem", () => {
  expect(folderListing({ path: "/srv/work", exists: true, isDir: true, entries: ["a", "b"] }))
    .toEqual({ entries: ["a", "b"], problem: "" });
});

test("an empty folder is not a problem", () => {
  expect(folderListing({ path: "/srv/work", exists: true, isDir: true, entries: [] }))
    .toEqual({ entries: [], problem: "" });
});

test("a relative path is refused with what to type instead", () => {
  expect(folderListing({ path: "", exists: false, isDir: false, entries: [] }).problem)
    .toBe("Type a full path, starting with / or ~.");
});

test("a folder that does not exist says so", () => {
  expect(folderListing({ path: "/nope", exists: false, isDir: false, entries: [] }).problem)
    .toBe("This folder does not exist.");
});

test("a file is not a folder", () => {
  expect(folderListing({ path: "/etc/hosts", exists: true, isDir: false, entries: [] }).problem)
    .toBe("This is a file, not a folder.");
});

test("an unreadable response is a failure, never a silent empty list", () => {
  expect(folderListing(null).problem).toBe("moa could not read this folder.");
});
