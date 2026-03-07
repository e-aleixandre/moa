package tui

import "github.com/charmbracelet/lipgloss"

// Theme defines the color palette for the TUI.
// Follows the Catppuccin naming convention: base/surface for backgrounds,
// text/subtext/overlay for text hierarchy, and named accents.
//
// Set ActiveTheme and call RebuildUI() before the TUI starts to apply a new theme.
type Theme struct {
	// Backgrounds (darkest → lightest)
	Base    lipgloss.Color
	Mantle  lipgloss.Color
	Crust   lipgloss.Color
	Surface0 lipgloss.Color
	Surface1 lipgloss.Color
	Surface2 lipgloss.Color

	// Text hierarchy (brightest → dimmest)
	Text     lipgloss.Color
	Subtext1 lipgloss.Color
	Subtext0 lipgloss.Color
	Overlay2 lipgloss.Color
	Overlay1 lipgloss.Color
	Overlay0 lipgloss.Color

	// Accents
	Rosewater lipgloss.Color
	Flamingo  lipgloss.Color
	Pink      lipgloss.Color
	Mauve     lipgloss.Color
	Red       lipgloss.Color
	Maroon    lipgloss.Color
	Peach     lipgloss.Color
	Yellow    lipgloss.Color
	Green     lipgloss.Color
	Teal      lipgloss.Color
	Sky       lipgloss.Color
	Sapphire  lipgloss.Color
	Blue      lipgloss.Color
	Lavender  lipgloss.Color
}

// CatppuccinMocha is the default dark theme.
var CatppuccinMocha = Theme{
	Base:     lipgloss.Color("#1e1e2e"),
	Mantle:   lipgloss.Color("#181825"),
	Crust:    lipgloss.Color("#11111b"),
	Surface0: lipgloss.Color("#313244"),
	Surface1: lipgloss.Color("#45475a"),
	Surface2: lipgloss.Color("#585b70"),
	Text:     lipgloss.Color("#cdd6f4"),
	Subtext1: lipgloss.Color("#bac2de"),
	Subtext0: lipgloss.Color("#a6adc8"),
	Overlay2: lipgloss.Color("#9399b2"),
	Overlay1: lipgloss.Color("#7f849c"),
	Overlay0: lipgloss.Color("#6c7086"),
	Rosewater: lipgloss.Color("#f5e0dc"),
	Flamingo:  lipgloss.Color("#f2cdcd"),
	Pink:      lipgloss.Color("#f5c2e7"),
	Mauve:     lipgloss.Color("#cba6f7"),
	Red:       lipgloss.Color("#f38ba8"),
	Maroon:    lipgloss.Color("#eba0ac"),
	Peach:     lipgloss.Color("#fab387"),
	Yellow:    lipgloss.Color("#f9e2af"),
	Green:     lipgloss.Color("#a6e3a1"),
	Teal:      lipgloss.Color("#94e2d5"),
	Sky:       lipgloss.Color("#89dceb"),
	Sapphire:  lipgloss.Color("#74c7ec"),
	Blue:      lipgloss.Color("#89b4fa"),
	Lavender:  lipgloss.Color("#b4befe"),
}

// ActiveTheme is the current color palette used by all TUI styles.
// Change this and call RebuildUI() to apply a new theme.
var ActiveTheme = CatppuccinMocha
