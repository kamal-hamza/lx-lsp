# lx-lsp

Language Server Protocol (LSP) implementation for lx.

## Overview

`lx-lsp` provides IDE features for lx files through the Language Server Protocol, enabling rich editing experiences in any LSP-compatible editor.

## Features

- Real-time syntax validation
- Code completion
- Go to definition
- Hover information
- Document symbols
- Diagnostics

## Installation

### Using Go

```bash
go install github.com/kamal-hamza/lx-lsp@latest
```

### Download Binary

Download the latest release for your platform from the [releases page](https://github.com/kamal-hamza/lx-lsp/releases).

### Build from Source

```bash
git clone https://github.com/kamal-hamza/lx-lsp.git
cd lx-lsp
make build
```

The binary will be available in the `build/` directory.

## Usage

The language server communicates over stdio and is designed to be used by LSP clients (editors/IDEs).

### Editor Configuration

#### VSCode

Install the lx extension (if available) or configure manually in your `settings.json`:

```json
{
  "lx": {
    "languageServer": {
      "command": "lx-lsp"
    }
  }
}
```

#### Neovim

Using `nvim-lspconfig`:

```lua
local lspconfig = require('lspconfig')
local configs = require('lspconfig.configs')

if not configs.lx_lsp then
  configs.lx_lsp = {
    default_config = {
      cmd = {'lx-lsp'},
      filetypes = {'lx'},
      root_dir = lspconfig.util.root_pattern('.git'),
    },
  }
end

lspconfig.lx_lsp.setup{}
```

## Development

### Prerequisites

- Go 1.25.4 or later

### Building

```bash
make build
```

### Testing

```bash
make test
```

### Running

```bash
make run
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Related Projects

- [lx-cli](https://github.com/kamal-hamza/lx-cli) - Command line interface for lx
