package tui

import "fmt"

// glamourStyleJSON is the glamour style for markdown rendering.
// Rebuilt by RebuildUI() from the active Theme.
var glamourStyleJSON []byte

// buildGlamourStyle generates a glamour JSON style from the given Theme.
// Maps theme colors to chroma syntax-highlighting tokens following
// Catppuccin's token-to-color conventions.
func buildGlamourStyle(t Theme) []byte {
	return []byte(fmt.Sprintf(`{
  "document": {
    "block_prefix": "\n",
    "block_suffix": "\n",
    "color": %q,
    "margin": 0
  },
  "block_quote": {
    "indent": 1,
    "indent_token": "│ ",
    "color": %q
  },
  "paragraph": {},
  "list": {
    "level_indent": 2
  },
  "heading": {
    "block_suffix": "\n",
    "color": %q,
    "bold": true
  },
  "h1": {
    "prefix": " ",
    "suffix": " ",
    "color": %q,
    "background_color": %q,
    "bold": true
  },
  "h2": {
    "prefix": "## ",
    "color": %q
  },
  "h3": {
    "prefix": "### ",
    "color": %q
  },
  "h4": {
    "prefix": "#### ",
    "color": %q
  },
  "h5": {
    "prefix": "##### ",
    "color": %q
  },
  "h6": {
    "prefix": "###### ",
    "color": %q,
    "bold": false
  },
  "text": {},
  "strikethrough": {
    "crossed_out": true
  },
  "emph": {
    "italic": true
  },
  "strong": {
    "bold": true
  },
  "hr": {
    "color": %q,
    "format": "\n--------\n"
  },
  "item": {
    "block_prefix": "• "
  },
  "enumeration": {
    "block_prefix": ". "
  },
  "task": {
    "ticked": "[✓] ",
    "unticked": "[ ] "
  },
  "link": {
    "color": %q,
    "underline": true
  },
  "link_text": {
    "color": %q,
    "bold": true
  },
  "image": {
    "color": %q,
    "underline": true
  },
  "image_text": {
    "color": %q,
    "format": "🖼  {{.text}}"
  },
  "code": {
    "color": %q,
    "background_color": %q
  },
  "code_block": {
    "color": %q,
    "margin": 2,
    "chroma": {
      "text":                { "color": %q },
      "error":               { "color": %q },
      "comment":             { "color": %q, "italic": true },
      "comment_single":      { "color": %q, "italic": true },
      "comment_multiline":   { "color": %q, "italic": true },
      "comment_preproc":     { "color": %q, "italic": true },
      "comment_special":     { "color": %q, "italic": true },
      "keyword":             { "color": %q },
      "keyword_constant":    { "color": %q },
      "keyword_declaration": { "color": %q },
      "keyword_namespace":   { "color": %q },
      "keyword_pseudo":      { "color": %q },
      "keyword_reserved":    { "color": %q },
      "keyword_type":        { "color": %q },
      "operator":            { "color": %q, "bold": true },
      "operator_word":       { "color": %q, "bold": true },
      "name":                { "color": %q },
      "name_attribute":      { "color": %q },
      "name_builtin":        { "color": %q },
      "name_builtin_pseudo": { "color": %q },
      "name_class":          { "color": %q },
      "name_constant":       { "color": %q },
      "name_decorator":      { "color": %q, "bold": true },
      "name_entity":         { "color": %q },
      "name_exception":      { "color": %q },
      "name_function":       { "color": %q },
      "name_function_magic": { "color": %q },
      "name_label":          { "color": %q },
      "name_namespace":      { "color": %q },
      "name_property":       { "color": %q },
      "name_tag":            { "color": %q },
      "name_variable":       { "color": %q },
      "name_variable_class":    { "color": %q },
      "name_variable_global":   { "color": %q },
      "name_variable_instance": { "color": %q },
      "name_other":          { "color": %q },
      "literal":             { "color": %q },
      "literal_number":       { "color": %q },
      "literal_number_float": { "color": %q },
      "literal_number_hex":   { "color": %q },
      "literal_number_integer":  { "color": %q },
      "literal_number_oct":      { "color": %q },
      "literal_string":          { "color": %q },
      "literal_string_backtick": { "color": %q },
      "literal_string_char":     { "color": %q },
      "literal_string_double":   { "color": %q },
      "literal_string_single":   { "color": %q },
      "literal_string_escape":   { "color": %q },
      "literal_string_interpol": { "color": %q },
      "literal_string_other":    { "color": %q },
      "literal_string_regex":    { "color": %q },
      "literal_string_symbol":   { "color": %q },
      "literal_string_doc":      { "color": %q },
      "literal_string_heredoc":  { "color": %q },
      "generic_deleted":  { "color": %q },
      "generic_emph":     { "italic": true },
      "generic_inserted": { "color": %q },
      "generic_strong":   { "bold": true },
      "generic_subheading": { "color": %q, "bold": true },
      "background":       { "background_color": %q }
    }
  },
  "table": {},
  "definition_list": {},
  "definition_term": {},
  "definition_description": {
    "block_prefix": "\n🠶 "
  },
  "html_block": {},
  "html_span": {}
}`,
		// document.color
		string(t.Text),
		// block_quote.color
		string(t.Subtext0),
		// heading.color
		string(t.Lavender),
		// h1: color (inverted text on accent bg), background_color
		string(t.Base), string(t.Lavender),
		// h2-h6
		string(t.Blue),
		string(t.Sapphire),
		string(t.Teal),
		string(t.Teal),
		string(t.Overlay0),
		// hr
		string(t.Surface2),
		// link
		string(t.Blue),
		// link_text
		string(t.Sapphire),
		// image
		string(t.Pink),
		// image_text
		string(t.Pink),
		// code: color, background_color
		string(t.Text), string(t.Surface0),
		// code_block.color
		string(t.Text),
		// chroma tokens
		string(t.Text),      // text
		string(t.Red),       // error
		string(t.Overlay0),  // comment
		string(t.Overlay0),  // comment_single
		string(t.Overlay0),  // comment_multiline
		string(t.Overlay0),  // comment_preproc
		string(t.Overlay0),  // comment_special
		string(t.Mauve),     // keyword
		string(t.Peach),     // keyword_constant
		string(t.Red),       // keyword_declaration
		string(t.Teal),      // keyword_namespace
		string(t.Mauve),     // keyword_pseudo
		string(t.Mauve),     // keyword_reserved
		string(t.Yellow),    // keyword_type
		string(t.Sky),       // operator
		string(t.Sky),       // operator_word
		string(t.Text),      // name
		string(t.Blue),      // name_attribute
		string(t.Sky),       // name_builtin
		string(t.Sky),       // name_builtin_pseudo
		string(t.Yellow),    // name_class
		string(t.Yellow),    // name_constant
		string(t.Blue),      // name_decorator
		string(t.Teal),      // name_entity
		string(t.Peach),     // name_exception
		string(t.Blue),      // name_function
		string(t.Blue),      // name_function_magic
		string(t.Sky),       // name_label
		string(t.Peach),     // name_namespace
		string(t.Peach),     // name_property
		string(t.Mauve),     // name_tag
		string(t.Rosewater), // name_variable
		string(t.Rosewater), // name_variable_class
		string(t.Rosewater), // name_variable_global
		string(t.Rosewater), // name_variable_instance
		string(t.Text),      // name_other
		string(t.Text),      // literal
		string(t.Peach),     // literal_number
		string(t.Peach),     // literal_number_float
		string(t.Peach),     // literal_number_hex
		string(t.Peach),     // literal_number_integer
		string(t.Peach),     // literal_number_oct
		string(t.Green),     // literal_string
		string(t.Green),     // literal_string_backtick
		string(t.Green),     // literal_string_char
		string(t.Green),     // literal_string_double
		string(t.Green),     // literal_string_single
		string(t.Blue),      // literal_string_escape
		string(t.Green),     // literal_string_interpol
		string(t.Green),     // literal_string_other
		string(t.Teal),      // literal_string_regex
		string(t.Green),     // literal_string_symbol
		string(t.Overlay0),  // literal_string_doc
		string(t.Overlay0),  // literal_string_heredoc
		string(t.Red),       // generic_deleted
		string(t.Green),     // generic_inserted
		string(t.Peach),     // generic_subheading
		string(t.Surface0),  // background
	))
}
