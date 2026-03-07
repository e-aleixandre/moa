package tui

// glamourStyleJSON is a custom glamour style using Catppuccin Mocha colors.
// Chroma token colors are taken from chroma's built-in catppuccin-mocha style.
var glamourStyleJSON = []byte(`{
  "document": {
    "block_prefix": "\n",
    "block_suffix": "\n",
    "color": "#cdd6f4",
    "margin": 0
  },
  "block_quote": {
    "indent": 1,
    "indent_token": "│ ",
    "color": "#a6adc8"
  },
  "paragraph": {},
  "list": {
    "level_indent": 2
  },
  "heading": {
    "block_suffix": "\n",
    "color": "#b4befe",
    "bold": true
  },
  "h1": {
    "prefix": " ",
    "suffix": " ",
    "color": "#1e1e2e",
    "background_color": "#b4befe",
    "bold": true
  },
  "h2": {
    "prefix": "## ",
    "color": "#89b4fa"
  },
  "h3": {
    "prefix": "### ",
    "color": "#74c7ec"
  },
  "h4": {
    "prefix": "#### ",
    "color": "#94e2d5"
  },
  "h5": {
    "prefix": "##### ",
    "color": "#94e2d5"
  },
  "h6": {
    "prefix": "###### ",
    "color": "#6c7086",
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
    "color": "#585b70",
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
    "color": "#89b4fa",
    "underline": true
  },
  "link_text": {
    "color": "#74c7ec",
    "bold": true
  },
  "image": {
    "color": "#f5c2e7",
    "underline": true
  },
  "image_text": {
    "color": "#f5c2e7",
    "format": "🖼  {{.text}}"
  },
  "code": {
    "color": "#cdd6f4",
    "background_color": "#313244"
  },
  "code_block": {
    "color": "#cdd6f4",
    "margin": 2,
    "chroma": {
      "text": {
        "color": "#cdd6f4"
      },
      "error": {
        "color": "#f38ba8"
      },
      "comment": {
        "color": "#6c7086",
        "italic": true
      },
      "comment_single": {
        "color": "#6c7086",
        "italic": true
      },
      "comment_multiline": {
        "color": "#6c7086",
        "italic": true
      },
      "comment_preproc": {
        "color": "#6c7086",
        "italic": true
      },
      "comment_special": {
        "color": "#6c7086",
        "italic": true
      },
      "keyword": {
        "color": "#cba6f7"
      },
      "keyword_constant": {
        "color": "#fab387"
      },
      "keyword_declaration": {
        "color": "#f38ba8"
      },
      "keyword_namespace": {
        "color": "#94e2d5"
      },
      "keyword_pseudo": {
        "color": "#cba6f7"
      },
      "keyword_reserved": {
        "color": "#cba6f7"
      },
      "keyword_type": {
        "color": "#f9e2af"
      },
      "operator": {
        "color": "#89dceb",
        "bold": true
      },
      "operator_word": {
        "color": "#89dceb",
        "bold": true
      },
      "name": {
        "color": "#cdd6f4"
      },
      "name_attribute": {
        "color": "#89b4fa"
      },
      "name_builtin": {
        "color": "#89dceb"
      },
      "name_builtin_pseudo": {
        "color": "#89dceb"
      },
      "name_class": {
        "color": "#f9e2af"
      },
      "name_constant": {
        "color": "#f9e2af"
      },
      "name_decorator": {
        "color": "#89b4fa",
        "bold": true
      },
      "name_entity": {
        "color": "#94e2d5"
      },
      "name_exception": {
        "color": "#fab387"
      },
      "name_function": {
        "color": "#89b4fa"
      },
      "name_function_magic": {
        "color": "#89b4fa"
      },
      "name_label": {
        "color": "#89dceb"
      },
      "name_namespace": {
        "color": "#fab387"
      },
      "name_property": {
        "color": "#fab387"
      },
      "name_tag": {
        "color": "#cba6f7"
      },
      "name_variable": {
        "color": "#f5e0dc"
      },
      "name_variable_class": {
        "color": "#f5e0dc"
      },
      "name_variable_global": {
        "color": "#f5e0dc"
      },
      "name_variable_instance": {
        "color": "#f5e0dc"
      },
      "name_other": {
        "color": "#cdd6f4"
      },
      "literal": {
        "color": "#cdd6f4"
      },
      "literal_number": {
        "color": "#fab387"
      },
      "literal_number_float": {
        "color": "#fab387"
      },
      "literal_number_hex": {
        "color": "#fab387"
      },
      "literal_number_integer": {
        "color": "#fab387"
      },
      "literal_number_oct": {
        "color": "#fab387"
      },
      "literal_string": {
        "color": "#a6e3a1"
      },
      "literal_string_backtick": {
        "color": "#a6e3a1"
      },
      "literal_string_char": {
        "color": "#a6e3a1"
      },
      "literal_string_double": {
        "color": "#a6e3a1"
      },
      "literal_string_single": {
        "color": "#a6e3a1"
      },
      "literal_string_escape": {
        "color": "#89b4fa"
      },
      "literal_string_interpol": {
        "color": "#a6e3a1"
      },
      "literal_string_other": {
        "color": "#a6e3a1"
      },
      "literal_string_regex": {
        "color": "#94e2d5"
      },
      "literal_string_symbol": {
        "color": "#a6e3a1"
      },
      "literal_string_doc": {
        "color": "#6c7086"
      },
      "literal_string_heredoc": {
        "color": "#6c7086"
      },
      "generic_deleted": {
        "color": "#f38ba8"
      },
      "generic_emph": {
        "italic": true
      },
      "generic_inserted": {
        "color": "#a6e3a1"
      },
      "generic_strong": {
        "bold": true
      },
      "generic_subheading": {
        "color": "#fab387",
        "bold": true
      },
      "background": {
        "background_color": "#313244"
      }
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
}`)
