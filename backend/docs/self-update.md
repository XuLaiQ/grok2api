# Web Version Updates

Language: English | [简体中文](self-update.zh-CN.md)

Administrators can save a GitHub repository in **Settings -> About**, check its latest stable Release, and confirm installation. The backend downloads, verifies SHA-256, extracts, restarts, and restores the previous application if startup fails. Progress remains available when you leave and reopen the page.

## Initial Setup

Existing servers need one deployment containing this feature. The current Compose file builds locally:

```bash
docker compose up -d --build grok2api
```

The image starts with `--supervise` by default. `/app/data/updates` lives in the existing `grok2api-data` volume. No Docker socket, host scripts, or container build tools are required. Preserve this data volume.

The repository starts empty and does not automatically check or install the original project. Open **Settings -> About**, enter `owner/repository` or `https://github.com/owner/repository`, save, and check for updates. Only stable Releases from public GitHub repositories are supported. The server needs access to the GitHub API and Release download hosts.

## Publish a Version

`.github/workflows/release.yml` builds complete release bundles on pushed `v*` tags, independently of the repository owner.

1. Push the project to your GitHub repository and enable Actions.
2. Change the root `VERSION`, for example to `v3.1.6`, and commit it.
3. Create and push a tag exactly matching `VERSION`.
4. Wait for **Release bundles** to finish, then check and install from the console.

```bash
git tag v3.1.6
git push origin v3.1.6
```

Use a version newer than the running version. Source commits, branch pushes, and GitHub's automatically generated source archives alone are not installable updates. Do not overwrite published tags or bundles.

A stable Release contains:

```text
grok2api_v3.1.6_linux_amd64.tar.gz
grok2api_v3.1.6_linux_arm64.tar.gz
checksums.txt
```

Each archive contains `grok2api`, `VERSION`, and `frontend/dist/`. The workflow embeds the release tag in the backend and computes SHA-256 checksums. Installation checks the configured repository again and rejects changed versions, downgrades, checksum mismatches, incomplete bundles, and unsafe archive paths.

## Configuration

| Setting | Default | Purpose |
| --- | --- | --- |
| `updates.directory` | `./data/updates` | Repository configuration, status, and release files; relative to the config file |
| `GROK2API_UPDATE_DIR` | Unset | Override update storage; Docker defaults to `/app/data/updates` |
| `GROK2API_UPDATE_REPOSITORY` | Empty | Initial repository; configuration saved from the console takes precedence |

The selected application remains in the update directory across service restarts and container recreation. Web installation replaces application files; it does not change the container image tag.

Linux amd64/arm64 binary deployments can also use managed startup:

```bash
./grok2api --supervise --config /absolute/path/config.yaml
```

Keep the bootstrap binary and configuration at stable paths and grant the runtime user write access to update storage. Ordinary `go run` or unmanaged processes can configure and check a repository but cannot install through the console. Windows, macOS, and multiple replicas do not support this installation mode.

## Behavior and Limits

- An administrator starts each installation. Duplicate clicks cannot start concurrent installs. The old application continues serving during download and verification; switching briefly interrupts service and long connections may need retrying.
- Health checks verify the new process identity and completion of startup recovery, preventing an old or unprepared process from satisfying verification. Failed startup triggers an attempt to restore the previous application and clean up the failed release.
- Automatic rollback covers backend and frontend files only. It does not restore databases, media, or runtime settings. Back up data and use your deployment process for incompatible database migrations.
- Downloads interrupted by a service restart become failed jobs and can be retried. The page resumes polling server state and refreshes after confirming the target version is running successfully.
- The publisher controls the executable bundle. SHA-256 verifies download integrity; use a repository you trust and control.
- Operating system dependencies, the bootstrap supervisor itself, deployment configuration, and rolling upgrades across replicas still require your deployment platform or image upgrade. Historical release directories remain on disk; allow room for download and extraction.

## Admin API

All endpoints are under `/api/admin/v1` and require existing administrator authentication.

| Method and Path | Request | Response |
| --- | --- | --- |
| `GET /system/version` | None | Current version, Release metadata, repository, installation availability |
| `PUT /system/update/config` | `{"repository":"owner/repository"}` | Saved version information; an empty string clears the repository |
| `POST /system/update/check` | None | Latest check result |
| `GET /system/update/status` | None | Installation state persisted across page loads and restarts |
| `POST /system/update/install` | `{"version":"v3.1.6"}` | `202` and the accepted installation job |
