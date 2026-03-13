package planmode

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
)

// ensurePlanDir creates the plans subdirectory under the session dir.
func ensurePlanDir(sessionDir string) (string, error) {
	dir := filepath.Join(sessionDir, "plans")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("planmode: create plan dir: %w", err)
	}
	return dir, nil
}

// newPlanPath generates a unique plan file path under the session directory.
func newPlanPath(sessionDir string) (path string, slug string, err error) {
	dir, err := ensurePlanDir(sessionDir)
	if err != nil {
		return "", "", err
	}
	slug = generateSlug()
	path = filepath.Join(dir, slug+".md")
	return path, slug, nil
}

// generateSlug creates a random adjective-noun-verb slug.
func generateSlug() string {
	adj := adjectives[rand.IntN(len(adjectives))]
	noun := nouns[rand.IntN(len(nouns))]
	verb := verbs[rand.IntN(len(verbs))]
	return adj + "-" + noun + "-" + verb
}

var adjectives = []string{
	"bold", "bright", "calm", "clean", "cold", "cool", "crisp", "dark",
	"deep", "dry", "fair", "fast", "firm", "flat", "fresh", "full",
	"grand", "green", "half", "hard", "keen", "kind", "late", "lean",
	"light", "long", "loud", "mild", "neat", "new", "nice", "odd",
	"old", "pale", "plain", "prime", "pure", "quick", "rare", "raw",
	"real", "rich", "ripe", "safe", "sharp", "short", "slim", "slow",
	"small", "smart", "soft", "solid", "spare", "stark", "steep", "still",
	"strong", "sure", "sweet", "swift", "tall", "thin", "tight", "tiny",
	"true", "vast", "warm", "weak", "wide", "wild", "wise", "young",
}

var nouns = []string{
	"arch", "bark", "beam", "bell", "bird", "blade", "bloom", "bolt",
	"bone", "brook", "cape", "cave", "chain", "cliff", "cloud", "coast",
	"core", "craft", "crest", "crow", "crown", "dawn", "dome", "drift",
	"dust", "edge", "elm", "ember", "fern", "field", "flame", "flint",
	"ford", "forge", "frost", "gate", "gem", "glade", "gleam", "glen",
	"glow", "grove", "hawk", "haze", "helm", "hill", "holt", "horn",
	"isle", "jade", "keep", "knot", "lake", "lark", "leaf", "ledge",
	"loom", "marsh", "mast", "maze", "mist", "mold", "moon", "moss",
	"oak", "ore", "palm", "path", "peak", "pine", "pond", "port",
	"rain", "reef", "ridge", "ring", "rock", "root", "rose", "sage",
	"salt", "sand", "seed", "shade", "shard", "shell", "shore", "silk",
	"slate", "slope", "spark", "spire", "star", "stem", "stone", "storm",
	"tide", "trail", "vale", "vine", "wave", "well", "wind", "wood",
}

var verbs = []string{
	"bind", "bloom", "break", "build", "burn", "carve", "cast", "chase",
	"claim", "clash", "climb", "close", "craft", "cross", "crush", "curve",
	"dance", "dare", "dash", "delve", "dive", "draft", "drain", "draw",
	"drift", "drive", "dwell", "fade", "fall", "fetch", "find", "flash",
	"float", "flow", "fly", "fold", "forge", "form", "found", "frame",
	"gain", "gaze", "gleam", "glide", "grasp", "grind", "grow", "guard",
	"guide", "hatch", "heal", "hunt", "join", "keep", "kneel", "knit",
	"launch", "lead", "lean", "leap", "learn", "light", "link", "march",
	"meld", "mend", "merge", "mine", "mold", "mount", "move", "parse",
	"pass", "patch", "phase", "plant", "plumb", "pour", "press", "prove",
	"pull", "push", "quest", "raise", "reach", "reign", "rise", "roam",
	"scale", "scan", "scope", "scout", "seal", "seek", "shape", "shift",
	"shine", "span", "spark", "split", "steer", "stoke", "surge", "sweep",
	"swing", "trace", "track", "trail", "turn", "twist", "vault", "wade",
	"wake", "walk", "watch", "weave", "wield", "wind", "yield",
}
