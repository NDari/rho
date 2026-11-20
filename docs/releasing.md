# Releasing

Pushing a tag `vX.Y.Z` runs the release workflow, which builds the archives with GoReleaser and publishes them with `checksums.txt` (spec section 19). The GUI is released from its own repository, [NDari/rho-gui](https://github.com/NDari/rho-gui).

## Signing macOS releases

The macOS `rho` is signed and notarized when these repository secrets are set; otherwise it is unsigned (ad-hoc signed).

| Secret | Holds |
|---|---|
| `MACOS_SIGN_P12` | a Developer ID Application certificate with its private key, exported as `.p12` and base64 encoded |
| `MACOS_SIGN_PASSWORD` | the `.p12` password |
| `MACOS_NOTARY_KEY` | an App Store Connect API key (`.p8`), base64 encoded |
| `MACOS_NOTARY_KEY_ID` | that key's ID |
| `MACOS_NOTARY_ISSUER_ID` | the issuer ID shown with the key |
