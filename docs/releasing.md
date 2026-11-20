# Releasing

Pushing a tag `vX.Y.Z` runs the release workflow (spec section 19). GoReleaser builds the `rho` and `rho-gui` archives into a draft release; the `macos-app` job then builds `Rho.app` on a macOS runner, adds it and its checksum, and publishes the release.

If GitHub has no free macOS runner, the `macos-app` job fails without running and the release stays a draft. Rerun only that job (`gh run rerun <run id> --failed`); it uses the macOS archive the first job saved.

## Signing macOS releases

The macOS binaries and `Rho.app` are signed and notarized when these repository secrets are set; otherwise they are unsigned (ad-hoc signed).

| Secret | Holds |
|---|---|
| `MACOS_SIGN_P12` | a Developer ID Application certificate with its private key, exported as `.p12` and base64 encoded |
| `MACOS_SIGN_PASSWORD` | the `.p12` password |
| `MACOS_NOTARY_KEY` | an App Store Connect API key (`.p8`), base64 encoded |
| `MACOS_NOTARY_KEY_ID` | that key's ID |
| `MACOS_NOTARY_ISSUER_ID` | the issuer ID shown with the key |

GoReleaser signs and notarizes the `rho` and `rho-gui` binaries, and the `macos-app` job signs, notarizes and staples `Rho.app` (see `gui/build/darwin/bundle.sh`).
