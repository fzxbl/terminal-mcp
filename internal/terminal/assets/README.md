# Vendored terminal browser assets

These files are the browser runtime artifacts from the exact npm packages below.
The files are served from this process and are embedded into the Go binary.

| Package | Version | npm tarball SHA-1 | License file |
| --- | --- | --- | --- |
| `@xterm/xterm` | `6.0.0` | `93637b0f2ee3a70718b5746a27c9c506af16745b` | `LICENSE-xterm-6.0.0.txt` |
| `@xterm/addon-fit` | `0.11.0` | `ba4778b69fcc9044a060c2176bbe077657d7b37e` | `LICENSE-addon-fit-0.11.0.txt` |
| `@xterm/addon-webgl` | `0.19.0` | `02533c3f7c0af3ac9a44bbb095cbd47d48e90c25` | `LICENSE-addon-webgl-0.19.0.txt` |

Runtime files are intentionally kept to the minified browser bundles and xterm stylesheet; source maps and package source trees are not needed at runtime.
