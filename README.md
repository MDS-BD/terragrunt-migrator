# terragrunt-migrator

`terragrunt-migrator` is a standalone Go CLI for upgrading versioned Terraform components and reapplying Terragrunt deployments in batches. It updates configuration files and can run Terragrunt for matching deployments.

## Requirements

- Install Terragrunt separately and make `terragrunt` available on `PATH`.
- Install Terraform or OpenTofu separately if required by your Terragrunt configuration. Neither is bundled by this program or its release archives.
- Run the CLI from the root of the Terraform/Terragrunt repository it should inspect. State-based component resolution needs the relevant backend and provider credentials.

## Install

No Go or Homebrew release has been published yet. When a release is available, Homebrew installation is intended to use the public `mds-bd/tools` tap:

```sh
brew tap MDS-BD/tools
brew install --cask mds-bd/tools/terragrunt-migrator
```

This is an intended install path, not a claim that the tap or a release is currently published. To install from a local checkout with Go 1.27.1 or newer:

```sh
go install ./cmd/terragrunt-migrator
```

The executable is installed to `$(go env GOPATH)/bin` (or `GOBIN` when set). The Homebrew cask also provides `tgmigrator` as an alias for the same program.

## Commands

Run commands from the target infrastructure repository. Omitted upgrade values and approval choices can be entered interactively. See `--help` on the command and subcommands for all options.

```sh
terragrunt-migrator --help
terragrunt-migrator upgrade-component --component example-service --version-range '3.*' --target-version 4.0.1 --dry-run
terragrunt-migrator upgrade-lambdas --version-range '1.*' --target-version 2.0.0 --dry-run
terragrunt-migrator reapply --component example-service --dry-run
```

The examples use synthetic component names and versions. `upgrade-component` finds matching `terragrunt.hcl` files beneath the current directory and updates their matching component source versions. `upgrade-lambdas` finds shared Lambda module declarations and referencing deployments beneath the repository root, updates matching module versions, and runs the selected deployments. `reapply` runs selected deployments without editing their configuration; `--component` and `--regex` filters are optional and, when both are set, both must match.

### Changes and execution

- `--dry-run` avoids configuration edits and the normal subsequent init/apply steps. For supported state-backed components, the CLI can still run `terragrunt init` and `terragrunt show -json` to resolve existing values.
- `--auto-approve` accepts configuration-file changes without prompting.
- `--auto-apply` passes `-auto-approve` to Terragrunt apply. It is off by default.
- Supported state resolvers preserve the `APP_VERSION` for ECS components and Lambda code version, base path, and per-function package versions. Other components do not use these resolvers.
- Each selected deployment runs `terragrunt init -upgrade` before `terragrunt apply`; an init failure skips apply for that deployment.
- Ctrl+C interrupts the managed Terragrunt/Terraform process group. If a process must be forcibly stopped, inspect state-lock status before retrying.

## Development

The Go module requires Go 1.27.1 or newer:

```sh
go build -o terragrunt-migrator ./cmd/terragrunt-migrator
go test ./...
go vet ./...
```

## Releases and licensing

The public source repository and release URLs are intended to be `https://github.com/MDS-BD/terragrunt-migrator`. The repository is prepared for public distribution, but no remote, release tag, or published release is configured by this checkout.

Release archives include the project MIT license, third-party notices, and license materials. The notices identify third-party dependencies and describe how to access required source. See [LICENSE](LICENSE), [THIRD-PARTY-NOTICES.txt](THIRD-PARTY-NOTICES.txt), and the [license bundle](LICENSES/). For the canonical dependency-license audit procedure and public release requirements, see the [company-agnostic publication playbook](https://github.com/MDS-BD/homebrew-tools/blob/main/PUBLISHING.md#rerunning-the-license-audit).

## License

The project is licensed under the MIT License; third-party code and notices are governed by their respective licenses. See [LICENSE](LICENSE) and the accompanying license materials.
