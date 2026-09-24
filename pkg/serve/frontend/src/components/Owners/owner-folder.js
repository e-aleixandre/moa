// owner-folder.js — the New owner folder explorer's two decisions, kept out of
// the component so they can be tested without a DOM.

// startingFolder is where the explorer opens: the workspace root moa was
// started in, else the user's home, else "/". An empty start meant no listing
// was ever requested and the dialog opened on an empty, silent list.
export function startingFolder(caps) {
  return caps?.workspaceRoot || caps?.homeDir || "/";
}

// folderListing reads a /api/fs/complete answer for "list this directory".
// The endpoint answers 200 with an empty list for every refusal, so a folder
// that cannot be listed has to be told apart here from one that is empty.
export function folderListing(data) {
  if (!data || typeof data !== "object") return { entries: [], problem: "moa could not read this folder." };
  if (!data.path) return { entries: [], problem: "Type a full path, starting with / or ~." };
  if (!data.exists) return { entries: [], problem: "This folder does not exist." };
  if (!data.isDir) return { entries: [], problem: "This is a file, not a folder." };
  return { entries: Array.isArray(data.entries) ? data.entries : [], problem: "" };
}
