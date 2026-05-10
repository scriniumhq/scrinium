# Contributing to Scrinium

Thanks for your interest. Scrinium is in active development; the
on-disk format and public API may change without notice.

## How to contribute

1. Open an issue describing the change before writing code, unless
the fix is obvious (typo, doc tweak, one-line bug).
2. Keep PRs focused — one logical change per PR. Tests come with
the change in the same PR.
3. Run `make ci` locally; it must pass.
4. Match the existing code style — `gofmt`, no surprises.

## Licensing of contributions

By submitting a pull request to this repository, you agree to
license your contribution under the [Apache License 2.0](LICENSE),
the same license under which the project is distributed. This is
the standard "inbound = outbound" model used by most permissively
licensed Go projects, and it is also the default per
[GitHub's Terms of Service §D.6](https://docs.github.com/en/site-policy/github-terms/github-terms-of-service#6-contributions-under-repository-license).

You retain copyright over your contribution. Apache 2.0 grants the
project (and its users) the rights it needs without transferring
ownership.

## Sign-off (DCO)

Every commit must include a `Signed-off-by:` trailer that asserts
you have the right to submit the change under the project's license.
This is the [Developer Certificate of Origin 1.1](https://developercertificate.org/)
in its standard form.

Use `git commit -s` to add the trailer automatically:

Signed-off-by: Jane Doe <jane@example.com>

The email must match the one in your Git config (`git config user.email`).
Pull requests with unsigned commits will not be merged. If you
forgot to sign off, amend with `git commit --amend --signoff` and
force-push.

## What sign-off means

By signing off on a commit, you certify the four points of the
DCO:

1. The contribution was created in whole or in part by you, and you
have the right to submit it under the project's open-source
license.
2. Or: the contribution is based upon previous work covered by an
appropriate open-source license, and you have the right to
submit it (modified or unmodified) under the project's license.
3. Or: the contribution was provided directly to you by some other
person who certified (1) or (2), and you have not modified it.
4. You understand the contribution and its sign-off are public, and
that a record of it is maintained indefinitely as part of the
project history.

The full text is at https://developercertificate.org/.

## Code of conduct

Be respectful, on-topic, and assume good faith. Disagreements get
worked out through technical argument; ad hominem isn't useful.