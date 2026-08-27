# Contributing

Keep changes focused, tested, and consistent with the contracts in `spec/`.

1. Read the relevant product and runtime specification before changing behavior.
2. Run `./test.sh` before opening a change.
3. Add or update regression coverage when behavior changes.
4. Do not commit credentials, local sessions, generated binaries, live-provider
   output, personal prompts, or ad-hoc audit artifacts.
5. Keep the Web console as a local operator surface and keep provider-specific
   protocol behavior inside provider adapters.

For substantial changes, update the matching specification in the same commit.
Report suspected vulnerabilities privately by following [`SECURITY.md`](./SECURITY.md);
do not include vulnerability details or sensitive evidence in a public issue.
