import { createContext } from "preact";

// What the lab forces on the Composer under it: { recording, call }. Absent
// (null) everywhere else, where the doubles hand back the real hooks untouched.
export const LabVoice = createContext(null);
