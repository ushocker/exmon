# Development signing keys

Built in only with `go build -tags exmondev`. Create a pair with:

    exmonctl keys dev-init --private %USERPROFILE%\.exmon\dev-signing.pem

which writes the public key here as `config/dev.pem` and `release/dev.pem`. The `.pem` files in this folder are git-ignored; never put a private key here, and never ship an `exmondev` build to production PCs.
